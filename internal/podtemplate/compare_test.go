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

func TestSubset_IgnoresLiveOnlyDefaults(t *testing.T) {
	desired := baseTemplate()
	live := baseTemplate()
	// Server default not present in desired.
	live["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["imagePullPolicy"] = "IfNotPresent"
	live["spec"].(map[string]any)["restartPolicy"] = "Always"
	probe := map[string]any{
		"httpGet": map[string]any{"path": "/", "port": "http", "scheme": "HTTP"},
	}
	desired["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = map[string]any{
		"httpGet": map[string]any{"path": "/", "port": "http"},
	}
	live["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = probe
	if podtemplate.SubsetChanged(desired, live) {
		t.Fatal("live-only httpGet.scheme and imagePullPolicy must be ignored")
	}
}

func TestSubset_ResourcesChange(t *testing.T) {
	desired := baseTemplate()
	live := baseTemplate()
	desired["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "100m"},
	}
	if !podtemplate.SubsetChanged(desired, live) {
		t.Fatal("resources-only change must count as changed")
	}
}

func TestSubset_ProbeChange(t *testing.T) {
	desired := baseTemplate()
	live := baseTemplate()
	desired["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = map[string]any{
		"httpGet": map[string]any{"path": "/ready", "port": 8080},
	}
	if !podtemplate.SubsetChanged(desired, live) {
		t.Fatal("probe-only change must count as changed")
	}
}

func TestSubset_AnnotationChange(t *testing.T) {
	desired := baseTemplate()
	live := baseTemplate()
	desired["metadata"].(map[string]any)["annotations"] = map[string]any{"roll": "1"}
	if !podtemplate.SubsetChanged(desired, live) {
		t.Fatal("annotation change must count as changed")
	}
}

func TestSubset_ImagePullPolicyChangeNotHidden(t *testing.T) {
	desired := baseTemplate()
	live := baseTemplate()
	desired["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["imagePullPolicy"] = "Always"
	live["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["imagePullPolicy"] = "IfNotPresent"
	if !podtemplate.SubsetChanged(desired, live) {
		t.Fatal("explicit imagePullPolicy change must count as changed")
	}
}

func TestSubset_MissingLiveFailClosed(t *testing.T) {
	if !podtemplate.SubsetChanged(baseTemplate(), nil) {
		t.Fatal("missing live template must count as changed")
	}
}

func TestSubset_Identical(t *testing.T) {
	if podtemplate.SubsetChanged(baseTemplate(), baseTemplate()) {
		t.Fatal("identical templates must be unchanged")
	}
}

func TestDeepEqual_NormalizesNumbers(t *testing.T) {
	a := map[string]any{"spec": map[string]any{"terminationGracePeriodSeconds": float64(30)}}
	b := map[string]any{"spec": map[string]any{"terminationGracePeriodSeconds": 30}}
	if !podtemplate.DeepEqual(a, b) {
		t.Fatal("float64 30 and int 30 must be equal")
	}
}
