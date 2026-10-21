package impact

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestTolerationAllows_KubernetesSemantics(t *testing.T) {
	taint := map[string]any{"key": "verify11", "value": "yes", "effect": "NoSchedule"}
	cases := []struct {
		name string
		tol  map[string]any
		want bool
	}{
		{
			name: "key Exists without effect matches any effect",
			tol:  map[string]any{"key": "verify11", "operator": "Exists"},
			want: true,
		},
		{
			name: "empty key Exists tolerates all taints",
			tol:  map[string]any{"operator": "Exists"},
			want: true,
		},
		{
			name: "Equal with matching value no effect",
			tol:  map[string]any{"key": "verify11", "value": "yes"},
			want: true,
		},
		{
			name: "Equal with matching value and effect",
			tol:  map[string]any{"key": "verify11", "operator": "Equal", "value": "yes", "effect": "NoSchedule"},
			want: true,
		},
		{
			name: "Exists ignores value",
			tol:  map[string]any{"key": "verify11", "operator": "Exists", "value": "wrong"},
			want: true,
		},
		{
			name: "Equal wrong value",
			tol:  map[string]any{"key": "verify11", "value": "no"},
			want: false,
		},
		{
			name: "NoSchedule toleration does not match NoExecute taint",
			tol:  map[string]any{"key": "verify11", "operator": "Exists", "effect": "NoSchedule"},
			want: false, // tested separately with NoExecute effect arg
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effect := "NoSchedule"
			if tc.name == "NoSchedule toleration does not match NoExecute taint" {
				effect = "NoExecute"
			}
			got := tolerationAllows([]any{tc.tol}, taint, effect)
			if got != tc.want {
				t.Fatalf("tolerationAllows(%v) effect=%s got %v want %v", tc.tol, effect, got, tc.want)
			}
		})
	}
}

func TestMapStr_MissingIsEmptyNotNilString(t *testing.T) {
	m := map[string]any{"key": "verify11", "operator": "Exists"}
	if mapStr(m, "effect") != "" {
		t.Fatalf("missing effect want \"\", got %q", mapStr(m, "effect"))
	}
	if mapStr(m, "value") != "" {
		t.Fatalf("missing value want \"\", got %q", mapStr(m, "value"))
	}
	if mapStr(map[string]any{"effect": nil}, "effect") != "" {
		t.Fatalf("nil effect want \"\"")
	}
}

func TestScheduling_N_ExistsTolerationWithoutEffect_Pass(t *testing.T) {
	// T5/T6: tainted node + Exists toleration (no effect / empty key) must not false-BLOCK.
	for _, name := range []string{"key-exists", "tolerate-all"} {
		t.Run(name, func(t *testing.T) {
			var tols []any
			if name == "key-exists" {
				tols = []any{map[string]any{"key": "verify11", "operator": "Exists"}}
			} else {
				tols = []any{map[string]any{"operator": "Exists"}}
			}
			tpl := map[string]any{"spec": map[string]any{
				"tolerations": tols,
				"containers": []any{map[string]any{"name": "api", "resources": map[string]any{
					"requests": map[string]any{"cpu": "100m"},
				}}},
			}}
			base := &graph.Snapshot{
				ID: "n-tol", Phase: graph.PhaseBaseline,
				Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
				Nodes: []graph.Node{
					{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
					{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
						"allocatablePods": 110, "allocatableCPUMillis": 4000, "schedulable": true,
						"labels": map[string]any{"kubernetes.io/os": "linux"},
						"taints": []any{map[string]any{"key": "verify11", "value": "yes", "effect": "NoSchedule"}},
					}},
					{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
						Attributes: map[string]any{
							"replicas": 3, "type": "Deployment", "maxSurge": "1",
							"tolerations": tols, "podTemplate": tpl,
						}},
					{ID: "pod/shop/a0", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 100}},
					{ID: "pod/shop/a1", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 100}},
					{ID: "pod/shop/a2", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 100}},
				},
			}
			tplDesired := map[string]any{"spec": map[string]any{
				"tolerations": tols,
				"containers": []any{map[string]any{
					"name": "api",
					"env":  []any{map[string]any{"name": "X", "value": "1"}},
					"resources": map[string]any{"requests": map[string]any{"cpu": "100m"}},
				}},
			}}
			ch := &loader.ChangeEnvelope{
				Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
				Seeds: []string{"workload/shop/api"},
				PatchNodes: []graph.Node{{
					ID: "workload/shop/api",
					Attributes: map[string]any{
						"replicas": 3, "maxSurge": "1", "tolerations": tols,
						"podTemplate": tplDesired, "podTemplateChanged": true,
					},
				}},
			}
			prop := loader.ApplyChange(base, ch)
			v := SchedulingCapacity{}.Evaluate(Context{
				Baseline: base, Proposed: prop, Change: ch,
				BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
			})
			if v.Status != Pass {
				t.Fatalf("N %s: want PASS, got %s: %s evidence=%v", name, v.Status, v.Explanation, v.Evidence)
			}
		})
	}
}
