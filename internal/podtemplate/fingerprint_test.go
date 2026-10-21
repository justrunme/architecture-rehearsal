package podtemplate_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func TestAttrs_ImageAndEnv(t *testing.T) {
	tpl := map[string]any{
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":  "api",
					"image": "nginx:1.27",
					"env": []any{
						map[string]any{"name": "FEATURE_FLAGS", "value": "fast-checkout=on"},
						map[string]any{"name": "A", "value": "1"},
					},
				},
			},
		},
	}
	attrs := podtemplate.Attrs(tpl)
	if attrs["containerImages"] != "api=nginx:1.27" {
		t.Fatalf("images=%v", attrs["containerImages"])
	}
	env, _ := attrs["containerEnv"].(string)
	if env != "api:A=1|FEATURE_FLAGS=fast-checkout=on" {
		t.Fatalf("env=%q", env)
	}
}

func TestChanged(t *testing.T) {
	if podtemplate.Changed("a=x", "", "a=x", "") {
		t.Fatal("same images must be unchanged")
	}
	if !podtemplate.Changed("a=x", "", "a=y", "") {
		t.Fatal("image change must be detected")
	}
	if !podtemplate.Changed("a=x", "api:K=1", "a=x", "api:K=2") {
		t.Fatal("env change must be detected")
	}
	if !podtemplate.Changed("", "", "a=x", "api:K=1") {
		t.Fatal("missing baseline vs desired must count as changed")
	}
}