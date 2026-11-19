// Command docstorio is the Docstorio manager: it serves the web UI and API
// and supervises one Factorio headless server. It takes no arguments;
// configuration comes from the environment (DESIGN "Environment
// variables").
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docstorio/internal/api"
	"docstorio/internal/factorio"
	"docstorio/internal/jobs"
	"docstorio/internal/mods"
	"docstorio/internal/portal"
	"docstorio/internal/settings"
	"docstorio/web"
)

// version is the manager version baked in at build time with
// -ldflags "-X main.version=<version>". When set it takes precedence over
// DOCSTORIO_VERSION; when neither is set the version is "dev".
var version string

// shutdownTimeout bounds the whole graceful shutdown. It exceeds
// factorio.StopTimeout so Factorio gets its full save window.
const shutdownTimeout = factorio.StopTimeout + 30*time.Second

func main() {
	log.SetFlags(0)
	log.SetPrefix("docstorio: ")
	if err := run(); err != nil {
		log.Print(jobs.Redact(err.Error()))
		os.Exit(1)
	}
}

// run wires the manager together and runs it until SIGTERM/SIGINT
// (docs/ARCH.md "Startup sequence").
func run() error {
	env := settings.LoadEnv(os.Getenv)
	env.ManagerVersion = managerVersion(version, env.ManagerVersion)
	paths := settings.NewPaths(env.DataDir, env.FactorioDir)

	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("data folder: %w", err)
	}
	if err := factorio.Seed(paths); err != nil {
		return fmt.Errorf("seeding config files: %w", err)
	}
	store, err := settings.Open(paths)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}

	factorioVersion, err := bakedFactorioVersion(env)
	if err != nil {
		return err
	}

	logs := factorio.NewLogBuffer(jobs.Redact)
	logs.Eventf("docstorio %s starting (Factorio %s, data %s)", env.ManagerVersion, factorioVersion, paths.Data)

	jobManager := jobs.New(jobs.Redact)
	defer jobManager.Close()

	portalClient := portal.New(portal.Config{
		BaseURL:   env.PortalURL,
		AssetsURL: env.AssetsURL,
		CacheDir:  paths.Cache,
		Factorio:  factorioVersion,
		Version:   env.ManagerVersion,
	})

	modManager, err := mods.New(mods.Config{
		ModsDir:         paths.Mods,
		FactorioDataDir: paths.FactorioData,
		StatePath:       paths.ModState(),
		ModListPath:     paths.ModList(),
		Factorio:        factorioVersion,
		Source:          portalClient,
	})
	if err != nil {
		return fmt.Errorf("mods: %w", err)
	}

	supervisor := factorio.NewSupervisor(factorio.Config{
		Paths:        paths,
		Settings:     store.Get,
		WriteModList: modManager.WriteModList,
		Log:          logs,
	})

	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		return fmt.Errorf("embedded UI: %w", err)
	}

	srv := &api.Server{
		Env:             env,
		Paths:           paths,
		Settings:        store,
		Jobs:            jobManager,
		Mods:            modManager,
		Portal:          portalClient,
		Factorio:        supervisor,
		Config:          factorio.NewConfigFiles(paths),
		Logs:            logs,
		FactorioVersion: factorioVersion,
		Static:          static,
	}

	ln, err := net.Listen("tcp", env.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", env.HTTPAddr, err)
	}
	httpServer := &http.Server{Handler: srv.Routes(), ReadHeaderTimeout: 10 * time.Second}
	httpErr := make(chan error, 1)
	go func() { httpErr <- httpServer.Serve(ln) }()
	logs.Eventf("web UI listening on %s", env.HTTPAddr)

	go autostart(jobManager, supervisor, logs)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var serveErr error
	select {
	case <-ctx.Done():
		logs.Eventf("shutdown requested")
	case serveErr = <-httpErr:
		logs.Eventf("web server failed: %v", serveErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Factorio first: it saves on SIGINT, which is what matters most.
	if err := supervisor.Shutdown(shutdownCtx); err != nil {
		logs.Eventf("stopping Factorio: %v", err)
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logs.Eventf("stopping web server: %v", err)
	}
	jobManager.Close()
	logs.Eventf("bye")
	if serveErr != nil {
		return fmt.Errorf("web server: %w", serveErr)
	}
	return nil
}

// managerVersion returns the -ldflags version when set, otherwise the one
// from the environment (DOCSTORIO_VERSION or "dev").
func managerVersion(linked, fromEnv string) string {
	if linked != "" {
		return linked
	}
	return fromEnv
}

// bakedFactorioVersion returns FACTORIO_VERSION, falling back to the version
// in the installed base mod's info.json.
func bakedFactorioVersion(env settings.Env) (mods.Version, error) {
	if env.FactorioVersion != "" {
		if v, err := mods.ParseVersion(env.FactorioVersion); err == nil {
			return v, nil
		}
	}
	v, err := factorio.DetectVersion(env.FactorioDir)
	if err != nil {
		return mods.Version{}, fmt.Errorf("cannot determine the Factorio version: %w", err)
	}
	return v, nil
}

// autostart starts Factorio under the busy lock when the setting is on and a
// save exists (Supervisor.Autostart decides).
func autostart(jm *jobs.Manager, sup *factorio.Supervisor, logs *factorio.LogBuffer) {
	j, err := jm.TryBegin(jobs.KindAutostart)
	if err != nil {
		logs.Eventf("autostart skipped: %v", err)
		return
	}
	err = sup.Autostart()
	if err != nil {
		logs.Eventf("autostart: %v", err)
	}
	j.Finish(err)
}
