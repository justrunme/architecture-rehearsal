package argo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ManifestFetcher obtains rendered desired manifests for an Application revision.
type ManifestFetcher interface {
	Fetch(app ApplicationRef, destDir string) error
}

// CLIFetcher runs `argocd app manifests` (or ARGOCD_BINARY).
type CLIFetcher struct {
	Binary string
	Server string
	AuthToken string
	Insecure bool
	// ExtraArgs appended after subcommand (e.g. --grpc-web).
	ExtraArgs []string
}

// NewCLIFetcherFromEnv builds a CLI fetcher from common Argo CD env vars.
func NewCLIFetcherFromEnv() *CLIFetcher {
	bin := os.Getenv("ARGOCD_BINARY")
	if bin == "" {
		bin = "argocd"
	}
	f := &CLIFetcher{
		Binary:    bin,
		Server:    os.Getenv("ARGOCD_SERVER"),
		AuthToken: os.Getenv("ARGOCD_AUTH_TOKEN"),
	}
	if os.Getenv("ARGOCD_INSECURE") == "true" || os.Getenv("ARGOCD_INSECURE") == "1" {
		f.Insecure = true
	}
	if os.Getenv("ARGOCD_GRPC_WEB") == "true" || os.Getenv("ARGOCD_GRPC_WEB") == "1" {
		f.ExtraArgs = append(f.ExtraArgs, "--grpc-web")
	}
	return f
}

// Fetch writes multi-doc YAML from argocd into destDir/manifests.yaml.
func (f *CLIFetcher) Fetch(app ApplicationRef, destDir string) error {
	if app.Name == "" {
		return fmt.Errorf("argo: application name required")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	bin := f.Binary
	if bin == "" {
		bin = "argocd"
	}
	// Argo CD CLI resolves apps in other namespaces via "ns/name" (no --app-namespace on manifests).
	appArg := app.Name
	if app.Namespace != "" && !strings.Contains(app.Name, "/") {
		appArg = app.Namespace + "/" + app.Name
	}
	args := []string{"app", "manifests", appArg}
	if app.Revision != "" {
		args = append(args, "--revision", app.Revision)
	}
	if f.Server != "" {
		args = append(args, "--server", f.Server)
	}
	if f.Insecure {
		args = append(args, "--insecure")
	}
	args = append(args, f.ExtraArgs...)

	cmd := exec.Command(bin, args...)
	// Prefer ARGOCD_AUTH_TOKEN env over --auth-token (avoids process-list leakage).
	if f.AuthToken != "" {
		cmd.Env = append(os.Environ(), "ARGOCD_AUTH_TOKEN="+f.AuthToken)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("argo: %s app manifests %s: %w (%s)", bin, app.Name, err, strings.TrimSpace(stderr.String()))
	}
	out := filepath.Join(destDir, "manifests.yaml")
	if err := os.WriteFile(out, stdout.Bytes(), 0o644); err != nil {
		return err
	}
	return nil
}

// DirFetcher copies or uses an already-rendered desired directory (tests / offline).
type DirFetcher struct {
	SourceDir string
}

// Fetch ensures destDir contains YAML (copies from SourceDir when distinct).
func (d *DirFetcher) Fetch(_ ApplicationRef, destDir string) error {
	if d.SourceDir == "" {
		return fmt.Errorf("argo: desired directory required")
	}
	info, err := os.Stat(d.SourceDir)
	if err != nil {
		return fmt.Errorf("argo: desired dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("argo: desired path is not a directory: %s", d.SourceDir)
	}
	if filepath.Clean(d.SourceDir) == filepath.Clean(destDir) {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return copyYAMLTree(d.SourceDir, destDir)
}

func copyYAMLTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			rel, _ := filepath.Rel(src, path)
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		name := strings.ToLower(de.Name())
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
