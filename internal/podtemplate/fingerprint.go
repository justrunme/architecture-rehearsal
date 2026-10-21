// Package podtemplate extracts comparable fingerprints of Pod templates so
// change compile and impact detectors can tell whether a sync will roll pods.
package podtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Attrs returns fingerprint attributes for a Pod template object
// (Deployment/StatefulSet/DaemonSet spec.template).
//
// podTemplateHash is the decision key (full normalized template).
// containerImages / containerEnv are evidence-only summaries.
func Attrs(template map[string]any) map[string]any {
	if template == nil {
		return nil
	}
	out := map[string]any{}
	if h := Hash(template); h != "" {
		out["podTemplateHash"] = h
	}
	tspec, _ := template["spec"].(map[string]any)
	if tspec != nil {
		if imgs := imagesFingerprint(tspec); imgs != "" {
			out["containerImages"] = imgs
		}
		if env := envFingerprint(tspec); env != "" {
			out["containerEnv"] = env
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Hash returns a stable hex SHA-256 of the normalized pod template.
// Returns "" when template is nil/empty after normalization.
func Hash(template map[string]any) string {
	n := Normalize(template)
	if n == nil || len(n) == 0 {
		return ""
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// HashChanged reports whether a sync should be treated as triggering a pod
// rollout. Fail-closed: a missing hash on either side counts as changed.
func HashChanged(baseHash, desireHash string) bool {
	if baseHash == "" || desireHash == "" {
		return true
	}
	return baseHash != desireHash
}

// Normalize returns a deep copy of template with server-populated defaults,
 // empty objects, and nulls stripped, and numeric types canonicalized so that
 // a live object (JSON float64s + API defaults) hashes the same as an equivalent
 // desired manifest (YAML ints, defaults omitted).
func Normalize(template map[string]any) map[string]any {
	if template == nil {
		return nil
	}
	cloned, ok := deepCopy(template).(map[string]any)
	if !ok || cloned == nil {
		return nil
	}
	normalizeTemplate(cloned)
	cleaned := removeEmpty(cloned)
	out, _ := cleaned.(map[string]any)
	return out
}

func normalizeTemplate(tpl map[string]any) {
	if meta, ok := tpl["metadata"].(map[string]any); ok {
		stripKeys(meta,
			"creationTimestamp", "resourceVersion", "uid", "generation",
			"managedFields", "selfLink", "ownerReferences",
			"generation", "generateName",
		)
		// Drop empty nested objects under metadata after strip.
	}
	spec, _ := tpl["spec"].(map[string]any)
	if spec == nil {
		return
	}
	stripPodSpecDefaults(spec)
	for _, key := range []string{"initContainers", "containers", "ephemeralContainers"} {
		list, _ := spec[key].([]any)
		for _, c := range list {
			if cm, ok := c.(map[string]any); ok {
				stripContainerDefaults(cm)
			}
		}
	}
}

func stripPodSpecDefaults(spec map[string]any) {
	removeIfEqual(spec, "restartPolicy", "Always")
	removeIfEqual(spec, "dnsPolicy", "ClusterFirst")
	removeIfEqual(spec, "schedulerName", "default-scheduler")
	removeIfEqual(spec, "terminationGracePeriodSeconds", int64(30))
	removeIfEqual(spec, "terminationGracePeriodSeconds", 30)
	removeIfEqual(spec, "terminationGracePeriodSeconds", float64(30))
	removeIfEqual(spec, "enableServiceLinks", true)
	removeIfEqual(spec, "preemptionPolicy", "PreemptLowerPriority")
	removeIfEqual(spec, "priority", int64(0))
	removeIfEqual(spec, "priority", 0)
	removeIfEqual(spec, "priority", float64(0))
	// API often mirrors serviceAccountName into serviceAccount.
	if san, ok := spec["serviceAccountName"]; ok {
		if sa, ok := spec["serviceAccount"]; ok && fmt.Sprint(sa) == fmt.Sprint(san) {
			delete(spec, "serviceAccount")
		}
	}
	delete(spec, "deprecatedServiceAccount")
	stripEmptyMap(spec, "securityContext")
	stripEmptyMap(spec, "affinity")
	stripEmptyMap(spec, "dnsConfig")
	stripEmptySlice(spec, "tolerations")
	stripEmptySlice(spec, "imagePullSecrets")
	stripEmptySlice(spec, "hostAliases")
	stripEmptySlice(spec, "topologySpreadConstraints")
}

func stripContainerDefaults(c map[string]any) {
	removeIfEqual(c, "imagePullPolicy", "IfNotPresent")
	removeIfEqual(c, "imagePullPolicy", "Always")
	removeIfEqual(c, "terminationMessagePath", "/dev/termination-log")
	removeIfEqual(c, "terminationMessagePolicy", "File")
	stripEmptyMap(c, "resources")
	stripEmptyMap(c, "securityContext")
	stripEmptyMap(c, "lifecycle")
	stripEmptySlice(c, "volumeMounts")
	stripEmptySlice(c, "envFrom")
	stripEmptySlice(c, "ports")
	stripEmptySlice(c, "env")
	for _, probe := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
		if p, ok := c[probe].(map[string]any); ok {
			stripProbeDefaults(p)
			cleaned := removeEmpty(p)
			cm, _ := cleaned.(map[string]any)
			if cm == nil || len(cm) == 0 {
				delete(c, probe)
			} else {
				c[probe] = cm
			}
		}
	}
	if ports, ok := c["ports"].([]any); ok {
		for _, p := range ports {
			if pm, ok := p.(map[string]any); ok {
				removeIfEqual(pm, "protocol", "TCP")
			}
		}
	}
}

func stripProbeDefaults(p map[string]any) {
	removeIfEqual(p, "timeoutSeconds", int64(1))
	removeIfEqual(p, "timeoutSeconds", 1)
	removeIfEqual(p, "timeoutSeconds", float64(1))
	removeIfEqual(p, "periodSeconds", int64(10))
	removeIfEqual(p, "periodSeconds", 10)
	removeIfEqual(p, "periodSeconds", float64(10))
	removeIfEqual(p, "successThreshold", int64(1))
	removeIfEqual(p, "successThreshold", 1)
	removeIfEqual(p, "successThreshold", float64(1))
	removeIfEqual(p, "failureThreshold", int64(3))
	removeIfEqual(p, "failureThreshold", 3)
	removeIfEqual(p, "failureThreshold", float64(3))
	removeIfEqual(p, "initialDelaySeconds", int64(0))
	removeIfEqual(p, "initialDelaySeconds", 0)
	removeIfEqual(p, "initialDelaySeconds", float64(0))
}

func removeIfEqual(m map[string]any, key string, want any) {
	v, ok := m[key]
	if !ok {
		return
	}
	if valuesEqual(normalizeScalar(v), normalizeScalar(want)) {
		delete(m, key)
	}
}

func valuesEqual(a, b any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func stripEmptyMap(m map[string]any, key string) {
	v, ok := m[key]
	if !ok {
		return
	}
	sm, ok := v.(map[string]any)
	if !ok || len(sm) == 0 {
		delete(m, key)
		return
	}
	// resources: {} or securityContext with only empty nested maps
	cleaned := removeEmpty(sm)
	cm, _ := cleaned.(map[string]any)
	if cm == nil || len(cm) == 0 {
		delete(m, key)
		return
	}
	m[key] = cm
}

func stripEmptySlice(m map[string]any, key string) {
	v, ok := m[key]
	if !ok {
		return
	}
	s, ok := v.([]any)
	if !ok || len(s) == 0 {
		delete(m, key)
	}
}

func stripKeys(m map[string]any, keys ...string) {
	for _, k := range keys {
		delete(m, k)
	}
}

func normalizeScalar(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case float32:
		f := float64(t)
		if f == float64(int64(f)) {
			return int64(f)
		}
		return f
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return string(t)
	default:
		return v
	}
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return normalizeScalar(t)
	}
}

func removeEmpty(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			cleaned := removeEmpty(val)
			if cleaned == nil {
				continue
			}
			if sm, ok := cleaned.(map[string]any); ok && len(sm) == 0 {
				continue
			}
			if sl, ok := cleaned.([]any); ok && len(sl) == 0 {
				continue
			}
			out[k] = cleaned
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			if val == nil {
				continue
			}
			cleaned := removeEmpty(val)
			if cleaned == nil {
				continue
			}
			out = append(out, cleaned)
		}
		return out
	default:
		return normalizeScalar(t)
	}
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
