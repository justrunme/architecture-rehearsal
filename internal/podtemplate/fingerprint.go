// Package podtemplate extracts comparable fingerprints of Pod templates so
// change compile and impact detectors can tell whether a sync will roll pods.
package podtemplate

import (
	"fmt"
	"sort"
	"strings"
)

// Attrs returns fingerprint attributes for a Pod template object
// (the value of Deployment/StatefulSet/DaemonSet spec.template).
// Keys: containerImages, containerEnv.
func Attrs(template map[string]any) map[string]any {
	if template == nil {
		return nil
	}
	tspec, _ := template["spec"].(map[string]any)
	if tspec == nil {
		return nil
	}
	out := map[string]any{}
	if imgs := imagesFingerprint(tspec); imgs != "" {
		out["containerImages"] = imgs
	}
	if env := envFingerprint(tspec); env != "" {
		out["containerEnv"] = env
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Changed reports whether desired template fingerprints differ from baseline.
// Empty-vs-empty is unchanged; empty baseline vs non-empty desired counts as
// changed (fail-closed: treat as a rollout trigger when live data was missing).
func Changed(baseImages, baseEnv, desireImages, desireEnv string) bool {
	return desireImages != baseImages || desireEnv != baseEnv
}

func imagesFingerprint(podSpec map[string]any) string {
	var parts []string
	for _, key := range []string{"initContainers", "containers"} {
		list, _ := podSpec[key].([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			name := fmt.Sprint(cm["name"])
			image := fmt.Sprint(cm["image"])
			if name == "" && image == "" {
				continue
			}
			parts = append(parts, name+"="+image)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func envFingerprint(podSpec map[string]any) string {
	var containerParts []string
	for _, key := range []string{"initContainers", "containers"} {
		list, _ := podSpec[key].([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			cname := fmt.Sprint(cm["name"])
			envList, _ := cm["env"].([]any)
			if len(envList) == 0 {
				continue
			}
			var evs []string
			for _, e := range envList {
				em, _ := e.(map[string]any)
				if em == nil {
					continue
				}
				ename := fmt.Sprint(em["name"])
				if ename == "" {
					continue
				}
				if v, ok := em["value"]; ok {
					evs = append(evs, ename+"="+fmt.Sprint(v))
					continue
				}
				if vf, ok := em["valueFrom"].(map[string]any); ok {
					evs = append(evs, ename+"=@"+valueFromRef(vf))
				}
			}
			if len(evs) == 0 {
				continue
			}
			sort.Strings(evs)
			containerParts = append(containerParts, cname+":"+strings.Join(evs, "|"))
		}
	}
	sort.Strings(containerParts)
	return strings.Join(containerParts, ";")
}

func valueFromRef(vf map[string]any) string {
	if s, ok := vf["secretKeyRef"].(map[string]any); ok {
		return "secret/" + fmt.Sprint(s["name"]) + "/" + fmt.Sprint(s["key"])
	}
	if c, ok := vf["configMapKeyRef"].(map[string]any); ok {
		return "configmap/" + fmt.Sprint(c["name"]) + "/" + fmt.Sprint(c["key"])
	}
	if f, ok := vf["fieldRef"].(map[string]any); ok {
		return "field/" + fmt.Sprint(f["fieldPath"])
	}
	if r, ok := vf["resourceFieldRef"].(map[string]any); ok {
		return "resource/" + fmt.Sprint(r["resource"])
	}
	return "valueFrom"
}
