package main

import (
	"testing"

	"docstorio/internal/mods"
	"docstorio/internal/settings"
)

func TestManagerVersion(t *testing.T) {
	if got := managerVersion("1.2.3", "0.1.0"); got != "1.2.3" {
		t.Fatalf("ldflags version not preferred: %q", got)
	}
	if got := managerVersion("", "0.1.0"); got != "0.1.0" {
		t.Fatalf("env version not used: %q", got)
	}
	if got := managerVersion("", "dev"); got != "dev" {
		t.Fatalf("default lost: %q", got)
	}
}

func TestBakedFactorioVersionFromEnv(t *testing.T) {
	env := settings.Env{FactorioVersion: "2.1.24", FactorioDir: t.TempDir()}
	v, err := bakedFactorioVersion(env)
	if err != nil {
		t.Fatal(err)
	}
	if v != (mods.Version{Major: 2, Minor: 1, Patch: 24}) {
		t.Fatalf("version = %+v", v)
	}
}
