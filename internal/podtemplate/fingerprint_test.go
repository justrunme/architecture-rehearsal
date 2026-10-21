package podtemplate_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func baseTemplate() map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{"app": "api"},
		},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":  "api",
					"image": "nginx:1.27",
				},
			},
		},
	}
}

func TestAttrs_IncludesHashAndEvidence(t *testing.T) {
	tpl := baseTemplate()
	tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
		map[string]any{"name": "FEATURE_FLAGS", "value": "fast-checkout=on"},
		map[string]any{"name": "A", "value": "1"},
	}
	attrs := podtemplate.Attrs(tpl)
	if attrs["podTemplateHash"] == nil || attrs["podTemplateHash"] == "" {
		t.Fatal("expected podTemplateHash")
	}
	if attrs["containerImages"] != "api=nginx:1.27" {
		t.Fatalf("images=%v", attrs["containerImages"])
	}
	env, _ := attrs["containerEnv"].(string)
	if env != "api:A=1|FEATURE_FLAGS=fast-checkout=on" {
		t.Fatalf("env=%q", env)
	}
}

func TestHashChanged_FailClosed(t *testing.T) {
	if !podtemplate.HashChanged("", "abc") {
		t.Fatal("missing baseline hash must count as changed")
	}
	if !podtemplate.HashChanged("abc", "") {
		t.Fatal("missing desired hash must count as changed")
	}
	if !podtemplate.HashChanged("", "") {
		t.Fatal("both missing must count as changed (fail-closed)")
	}
	if podtemplate.HashChanged("abc", "abc") {
		t.Fatal("equal hashes must be unchanged")
	}
	if !podtemplate.HashChanged("abc", "def") {
		t.Fatal("unequal hashes must be changed")
	}
}

func TestHash_ResourcesChangeDiffers(t *testing.T) {
	a := baseTemplate()
	b := baseTemplate()
	b["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
	}
	if podtemplate.Hash(a) == podtemplate.Hash(b) {
		t.Fatal("resources-only change must change hash")
	}
}

func TestHash_ProbeChangeDiffers(t *testing.T) {
	a := baseTemplate()
	b := baseTemplate()
	b["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = map[string]any{
		"httpGet": map[string]any{"path": "/ready", "port": 8080},
	}
	if podtemplate.Hash(a) == podtemplate.Hash(b) {
		t.Fatal("probe-only change must change hash")
	}
}

func TestHash_AnnotationChangeDiffers(t *testing.T) {
	a := baseTemplate()
	b := baseTemplate()
	b["metadata"].(map[string]any)["annotations"] = map[string]any{
		"example.com/roll": "1",
	}
	if podtemplate.Hash(a) == podtemplate.Hash(b) {
		t.Fatal("template annotation change must change hash")
	}
}

func TestHash_LiveDefaultsMatchDesired(t *testing.T) {
	desired := baseTemplate()
	// Live object as returned by the API: defaults + float64 numbers + null timestamp.
	live := map[string]any{
		"metadata": map[string]any{
			"creationTimestamp": nil,
			"labels":            map[string]any{"app": "api"},
		},
		"spec": map[string]any{
			"restartPolicy":                 "Always",
			"dnsPolicy":                     "ClusterFirst",
			"schedulerName":                 "default-scheduler",
			"terminationGracePeriodSeconds": float64(30),
			"securityContext":               map[string]any{},
			"containers": []any{
				map[string]any{
					"name":                     "api",
					"image":                    "nginx:1.27",
					"imagePullPolicy":          "IfNotPresent",
					"terminationMessagePath":   "/dev/termination-log",
					"terminationMessagePolicy": "File",
					"resources":                map[string]any{},
				},
			},
		},
	}
	dh := podtemplate.Hash(desired)
	lh := podtemplate.Hash(live)
	if dh == "" || lh == "" {
		t.Fatalf("empty hash desired=%q live=%q", dh, lh)
	}
	if dh != lh {
		t.Fatalf("live defaults must hash equal to desired; desired=%s live=%s\nD=%v\nL=%v",
			dh, lh, podtemplate.Normalize(desired), podtemplate.Normalize(live))
	}
	if podtemplate.HashChanged(lh, dh) {
		t.Fatal("HashChanged must be false for live-defaults vs desired")
	}
}

func TestHash_IdenticalStable(t *testing.T) {
	a := baseTemplate()
	b := baseTemplate()
	if podtemplate.Hash(a) != podtemplate.Hash(b) {
		t.Fatal("identical templates must hash equal")
	}
}
