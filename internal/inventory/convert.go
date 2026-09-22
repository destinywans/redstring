package inventory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func obj(value any) map[string]any {
	if v, ok := value.(map[string]any); ok {
		return v
	}
	if v, ok := value.(object); ok {
		return map[string]any(v)
	}
	return map[string]any{}
}
func array(value any) []any {
	if v, ok := value.([]any); ok {
		return v
	}
	return nil
}
func str(value any) string {
	if v, ok := value.(string); ok {
		return v
	}
	return ""
}
func number(value any) float64 {
	if v, ok := value.(float64); ok {
		return v
	}
	return 0
}
func nested(value any, keys ...string) any {
	for _, key := range keys {
		value = obj(value)[key]
	}
	return value
}
func uid(o object) string       { return str(nested(o, "metadata", "uid")) }
func name(o object) string      { return str(nested(o, "metadata", "name")) }
func namespace(o object) string { return str(nested(o, "metadata", "namespace")) }
func owner(o object) string {
	for _, v := range array(nested(o, "metadata", "ownerReferences")) {
		if obj(v)["controller"] == true {
			return str(obj(v)["uid"])
		}
	}
	return ""
}
func condition(status map[string]any, kind string) string {
	for _, v := range array(status["conditions"]) {
		if str(obj(v)["type"]) == kind {
			return str(obj(v)["status"])
		}
	}
	return "Unknown"
}
func isController(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
		return true
	}
	return false
}
func matchesLabels(labels map[string]any, selector map[string]string) bool {
	for key, value := range selector {
		if str(labels[key]) != value {
			return false
		}
	}
	return true
}
func images(o object) []string {
	spec := obj(o["spec"])
	if str(o["kind"]) != "Pod" {
		spec = obj(nested(spec, "template", "spec"))
	}
	if str(o["kind"]) == "CronJob" {
		spec = obj(nested(o, "spec", "jobTemplate", "spec", "template", "spec"))
	}
	out := []string{}
	for _, v := range array(spec["containers"]) {
		out = append(out, str(obj(v)["image"]))
	}
	return out
}
func links(source Source, o object) []Link {
	out := []Link{}
	seen := map[string]bool{}
	labels := obj(nested(o, "metadata", "labels"))
	for k, v := range obj(nested(o, "spec", "template", "metadata", "labels")) {
		labels[k] = v
	}
	for _, binding := range source.ApplicationBindings {
		if binding.Namespace != "" && binding.Namespace != "*" && binding.Namespace != namespace(o) {
			continue
		}
		if !matchesLabels(labels, binding.LabelSelector) {
			continue
		}
		key := binding.ApplicationID + ":" + binding.EnvironmentID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Link{binding.ApplicationID, binding.EnvironmentID})
	}
	return out
}
func convert(source Source, objects []object, observed time.Time) (Snapshot, error) {
	snapshot := Snapshot{SourceID: source.ID, SourceKind: "kubernetes", SourceName: source.Name, ObservedAt: observed, GrantPermissionIDs: source.GrantPermissionIDs, Resources: []Record{}, Relations: []Edge{}}
	byUID := map[string]object{}
	namespaces := map[string]string{}
	nodes := map[string]string{}
	pvcs := map[string]string{}
	pvs := map[string]string{}
	classes := map[string]string{}
	clusterUID := ""
	for _, o := range objects {
		if uid(o) == "" {
			return snapshot, fmt.Errorf("Kubernetes object UID missing")
		}
		byUID[uid(o)] = o
		switch str(o["kind"]) {
		case "Namespace":
			namespaces[name(o)] = uid(o)
			if name(o) == "kube-system" {
				clusterUID = "cluster:" + uid(o)
			}
		case "Node":
			nodes[name(o)] = uid(o)
		case "PersistentVolumeClaim":
			pvcs[namespace(o)+"/"+name(o)] = uid(o)
		case "PersistentVolume":
			pvs[name(o)] = uid(o)
		case "StorageClass":
			classes[name(o)] = uid(o)
		}
	}
	if clusterUID == "" {
		return snapshot, fmt.Errorf("kube-system namespace UID unavailable")
	}
	addEdge := func(a, b, kind string) {
		if a != "" && b != "" && a != b {
			snapshot.Relations = append(snapshot.Relations, Edge{a, b, kind})
		}
	}
	// Runtime Pods inherit exactly one true controller owner. ReplicaSet/Job
	// intermediate owners are followed to their top controller, bounded by depth.
	topOwner := func(o object) string {
		parent := owner(o)
		seen := map[string]bool{}
		for i := 0; i < 8 && parent != ""; i++ {
			if seen[parent] {
				return ""
			}
			seen[parent] = true
			p, exists := byUID[parent]
			if !exists || !isController(str(p["kind"])) {
				return ""
			}
			next := owner(p)
			if next == "" {
				return parent
			}
			parent = next
		}
		return ""
	}
	readyNodes := 0
	for _, o := range objects {
		if str(o["kind"]) == "Node" && condition(obj(o["status"]), "Ready") == "True" {
			readyNodes++
		}
	}
	clusterStatus := "Unknown"
	if len(nodes) > 0 {
		clusterStatus = "NotReady"
		if readyNodes == len(nodes) {
			clusterStatus = "Ready"
		}
	}
	snapshot.Resources = append(snapshot.Resources, Record{ExternalID: clusterUID, Kind: "cluster", Name: source.Name, Description: "Kubernetes API 实时资源目录", ObservedAt: observed, Applications: links(source, object{}), Attributes: map[string]any{"cluster": source.Name, "environment_key": source.EnvironmentKey, "nodes_count": len(nodes), "ready_nodes": readyNodes, "status": clusterStatus, "provider": "Kubernetes API", "resource_type": "Cluster"}})
	for _, o := range objects {
		k := str(o["kind"])
		meta, spec, status := obj(o["metadata"]), obj(o["spec"]), obj(o["status"])
		attrs := map[string]any{"cluster": source.Name, "environment_key": source.EnvironmentKey, "namespace": namespace(o), "resource_type": k, "uid": uid(o), "resource_version": str(meta["resourceVersion"]), "created_at": meta["creationTimestamp"], "labels": meta["labels"], "status": "Unknown"}
		// No Secret, ConfigMap, Pod env, command, args or annotations are collected.
		record := Record{ExternalID: uid(o), Name: name(o), Attributes: attrs, ObservedAt: observed, Applications: links(source, o)}
		if timestamp, err := time.Parse(time.RFC3339, str(meta["creationTimestamp"])); err == nil {
			attrs["age"] = observed.Sub(timestamp).Round(time.Second).String()
		}
		addEdge(uid(o), clusterUID, "belongs_to")
		if namespace(o) != "" {
			addEdge(uid(o), namespaces[namespace(o)], "belongs_to")
		}
		switch k {
		case "Namespace":
			record.Kind = "namespace"
			attrs["status"] = status["phase"]
			count := 0
			for _, child := range objects {
				if str(child["kind"]) == "Pod" && namespace(child) == name(o) {
					count++
				}
			}
			attrs["pods_count"] = count
		case "Node":
			record.Kind = "node"
			attrs["status"] = "NotReady"
			if condition(status, "Ready") == "True" {
				attrs["status"] = "Ready"
			}
			info := obj(status["nodeInfo"])
			attrs["version"] = info["kubeletVersion"]
			attrs["os"] = info["osImage"]
			attrs["container_runtime"] = info["containerRuntimeVersion"]
			attrs["conditions"] = status["conditions"]
			attrs["capacity"] = status["capacity"]
			attrs["allocatable"] = status["allocatable"]
			roles := []string{}
			for label := range obj(meta["labels"]) {
				if strings.HasPrefix(label, "node-role.kubernetes.io/") {
					roles = append(roles, strings.TrimPrefix(label, "node-role.kubernetes.io/"))
				}
			}
			if len(roles) == 0 {
				roles = []string{"worker"}
			}
			sort.Strings(roles)
			attrs["roles"] = roles
			for _, address := range array(status["addresses"]) {
				if str(obj(address)["type"]) == "InternalIP" {
					attrs["ip"] = obj(address)["address"]
				}
			}
			count := 0
			for _, pod := range objects {
				if str(pod["kind"]) == "Pod" && str(nested(pod, "spec", "nodeName")) == name(o) {
					count++
				}
			}
			attrs["pod_count"] = count
			if attrs["version"] != nil {
				snapshot.Resources[0].Attributes["version"] = attrs["version"]
			}
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
			record.Kind = "deployment"
			attrs["workload_type"] = k
			attrs["images"] = images(o)
			attrs["version"] = strings.Join(images(o), ", ")
			attrs["replicas"] = spec["replicas"]
			attrs["ready_replicas"] = number(status["readyReplicas"])
			attrs["status"] = "Progressing"
			desired := number(spec["replicas"])
			if spec["replicas"] == nil {
				desired = 1
			}
			ready := number(status["readyReplicas"])
			if k == "DaemonSet" {
				desired = number(status["desiredNumberScheduled"])
				ready = number(status["numberReady"])
			}
			attrs["replicas"] = desired
			attrs["ready_replicas"] = ready
			if ready >= desired && number(status["observedGeneration"]) >= number(meta["generation"]) {
				attrs["status"] = "Ready"
			}
			if k == "Job" {
				attrs["status"] = "Running"
				if condition(status, "Complete") == "True" {
					attrs["status"] = "Complete"
				}
				if condition(status, "Failed") == "True" {
					attrs["status"] = "Failed"
				}
			}
			if k == "CronJob" {
				attrs["schedule"] = spec["schedule"]
				attrs["status"] = "Active"
				if spec["suspend"] == true {
					attrs["status"] = "Suspended"
				}
			}
			if p := owner(o); p != "" {
				addEdge(uid(o), p, "belongs_to")
			}
		case "Pod":
			record.Kind = "pod"
			attrs["status"] = status["phase"]
			attrs["node"] = spec["nodeName"]
			attrs["ip"] = status["podIP"]
			attrs["images"] = images(o)
			attrs["conditions"] = status["conditions"]
			ready, restarts := 0, 0
			containers := []map[string]any{}
			for _, c := range array(status["containerStatuses"]) {
				v := obj(c)
				if v["ready"] == true {
					ready++
				}
				restarts += int(number(v["restartCount"]))
				state := obj(v["state"])
				reason := "Running"
				if wait := obj(state["waiting"]); len(wait) > 0 {
					reason = str(wait["reason"])
					attrs["status"] = reason
				}
				if term := obj(state["terminated"]); len(term) > 0 {
					reason = str(term["reason"])
				}
				containers = append(containers, map[string]any{"name": v["name"], "image": v["image"], "ready": v["ready"], "restarts": v["restartCount"], "status": reason})
			}
			attrs["ready"] = fmt.Sprintf("%d / %d", ready, len(array(spec["containers"])))
			attrs["restarts"] = restarts
			attrs["containers"] = containers
			addEdge(uid(o), nodes[str(spec["nodeName"])], "runs_on")
			if parent := topOwner(o); parent != "" {
				addEdge(uid(o), parent, "belongs_to")
				attrs["workload"] = name(byUID[parent])
				record.Applications = links(source, byUID[parent])
			} else if parent := owner(o); str(byUID[parent]["kind"]) == "Node" {
				addEdge(uid(o), parent, "runtime_owner")
			} else if owner(o) == "" {
				addEdge(uid(o), namespaces[namespace(o)], "runtime_owner")
			}
			for _, volume := range array(spec["volumes"]) {
				claim := str(nested(volume, "persistentVolumeClaim", "claimName"))
				if claim != "" {
					addEdge(uid(o), pvcs[namespace(o)+"/"+claim], "uses")
				}
			}
		case "Service", "Ingress":
			record.Kind = "network"
			attrs["service_type"] = spec["type"]
			attrs["status"] = "Observed"
			attrs["ports"] = spec["ports"]
			attrs["address"] = spec["clusterIP"]
			if k == "Ingress" {
				attrs["service_type"] = "Ingress"
				hosts := []string{}
				for _, rule := range array(spec["rules"]) {
					hosts = append(hosts, str(obj(rule)["host"]))
				}
				attrs["address"] = hosts
			}
			selector := map[string]string{}
			for key, v := range obj(spec["selector"]) {
				selector[key] = str(v)
			}
			workloads := map[string]bool{}
			if len(selector) > 0 {
				for _, pod := range objects {
					if str(pod["kind"]) == "Pod" && namespace(pod) == namespace(o) && matchesLabels(obj(nested(pod, "metadata", "labels")), selector) {
						addEdge(uid(o), uid(pod), "routes_to")
						if p := topOwner(pod); p != "" {
							addEdge(uid(o), p, "connects_to")
							workloads[name(byUID[p])] = true
						}
					}
				}
			}
			names := []string{}
			for n := range workloads {
				names = append(names, n)
			}
			sort.Strings(names)
			attrs["workload"] = names
		case "PersistentVolume", "PersistentVolumeClaim", "StorageClass":
			record.Kind = "storage"
			attrs["status"] = status["phase"]
			attrs["storage_class"] = spec["storageClassName"]
			attrs["capacity"] = spec["capacity"]
			if k == "PersistentVolumeClaim" {
				attrs["capacity"] = status["capacity"]
				addEdge(uid(o), pvs[str(spec["volumeName"])], "uses")
			}
			if k == "StorageClass" {
				attrs["status"] = "Observed"
				attrs["provisioner"] = o["provisioner"]
			}
			addEdge(uid(o), classes[str(spec["storageClassName"])], "uses")
		case "Event":
			record.Kind = "event"
			attrs["status"] = o["type"]
			attrs["severity"] = o["type"]
			attrs["reason"] = o["reason"]
			attrs["message"] = o["message"]
			attrs["count"] = o["count"]
			attrs["first_seen"] = o["firstTimestamp"]
			attrs["last_seen"] = o["lastTimestamp"]
			if o["lastTimestamp"] == nil {
				attrs["last_seen"] = o["eventTime"]
			}
			involved := obj(o["involvedObject"])
			attrs["involved_object"] = str(involved["kind"]) + "/" + str(involved["name"])
			record.Name = str(o["reason"]) + " · " + str(involved["name"])
			addEdge(uid(o), str(involved["uid"]), "involved_object")
		default:
			continue
		}
		snapshot.Resources = append(snapshot.Resources, record)
	}
	return snapshot, nil
}
