package util

import (
	autov2beta1 "k8s.io/api/autoscaling/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"reflect"
)

// Difference returns the difference of 2 []string slice
// diff1: in slice1, not in slice2
// diff2: not in slice1, in slice2
// https://stackoverflow.com/questions/19374219/how-to-find-the-difference-between-two-slices-of-strings-in-golang?answertab=votes#tab-top
func Difference(slice1 []string, slice2 []string) (diff1 []string, diff2 []string) {
	// Loop two times, first to find slice1 strings not in slice2,
	// second loop to find slice2 strings not in slice1
	for i := 0; i < 2; i++ {
		for _, s1 := range slice1 {
			found := false
			for _, s2 := range slice2 {
				if s1 == s2 {
					found = true
					break
				}
			}
			// String not found. We add it to return slice
			if !found {
				if i == 0 {
					diff1 = append(diff1, s1)
				} else {
					diff2 = append(diff2, s1)
				}
			}
		}
		// Swap the slices, only if it was the first loop
		if i == 0 {
			slice1, slice2 = slice2, slice1
		}
	}

	return
}

// Difference2 returns the difference of 2 []corev1.EnvVar slice
// diff1: in slice1(origin), not in slice2(now) delete
// diff2: not in slice1(origin), in slice2(now) add
// modified: the slice2 value
// https://stackoverflow.com/questions/19374219/how-to-find-the-difference-between-two-slices-of-strings-in-golang?answertab=votes#tab-top
func Difference2(origin []corev1.EnvVar, now []corev1.EnvVar) (diff1 []corev1.EnvVar,
	diff2 []corev1.EnvVar, modified []corev1.EnvVar) {
	// Avoid the keys duplicate
	cMap := make(map[string]string)
	// Loop two times, first to find slice1 strings not in slice2,
	// second loop to find slice2 strings not in slice1
	for i := 0; i < 2; i++ {
		for _, s1 := range origin {
			found := false
			for _, s2 := range now {
				if s1.Name == s2.Name {
					if !reflect.DeepEqual(s1, s2) {
						if i == 0 {
							modified = append(modified, s2)
						}
						cMap[s1.Name] = ""
					}
					found = true
					//break
				}
			}
			// String not found. We add it to return slice
			if !found {
				if i == 0 {
					diff1 = append(diff1, s1)
				} else {
					diff2 = append(diff2, s1)
				}
			}
		}
		// Swap the slices, only if it was the first loop
		if i == 0 {
			origin, now = now, origin
		}
	}

	//for key, _ := range cMap {
	//	conflict = append(conflict, key)
	//}

	return
}

// DifferenceVol look like the Difference2 but for VolumeMount
func DifferenceVol(origin, now []corev1.VolumeMount) (deleted, added, modified []corev1.VolumeMount) {
	// Avoid the keys duplicate
	cMap := make(map[string]string)

	// Loop two times, first to find slice1 strings not in slice2,
	// second loop to find slice2 strings not in slice1
	for i := 0; i < 2; i++ {
		for _, s1 := range origin {
			found := false
			for _, s2 := range now {
				if s1.Name == s2.Name {
					if s1.MountPath != s2.MountPath || s1.ReadOnly != s2.ReadOnly {
						if i == 0 {
							modified = append(modified, s2)
						}
						cMap[s1.Name] = ""
					}
					found = true
					break
				}
			}
			// String not found. We add it to return slice
			if !found {
				if i == 0 {
					deleted = append(deleted, s1)
				} else {
					added = append(added, s1)
				}
			}
		}
		// Swap the slices, only if it was the first loop
		if i == 0 {
			origin, now = now, origin
		}
	}
	return
}

// DifferenceMetric look like the Difference2 but for MetricSpec
func DifferenceMetric(origin, now []autov2beta1.MetricSpec) (deleted, added, modified []autov2beta1.MetricSpec) {
	// Avoid the keys duplicate
	cMap := make(map[string]string)

	// Loop two times, first to find slice1 strings not in slice2,
	// second loop to find slice2 strings not in slice1
	for i := 0; i < 2; i++ {
		for _, s1 := range origin {
			found := false
			for _, s2 := range now {
				if s1.Type == s2.Type {
					switch s1.Type {
					case autov2beta1.ObjectMetricSourceType:
						if s1.Object.MetricName == s2.Object.MetricName {
							if !reflect.DeepEqual(s1.Object, s2.Object) {
								if i == 0 {
									modified = append(modified, s2)
								}
								cMap[string(s1.Type)+"_"+s1.Object.MetricName] = ""
							}
							found = true
						}
						break
					case autov2beta1.ExternalMetricSourceType:
						if s1.External.MetricName == s2.External.MetricName {
							if !reflect.DeepEqual(s1.External, s2.External) {
								if i == 0 {
									modified = append(modified, s2)
								}
								cMap[string(s1.Type)+"_"+s1.External.MetricName] = ""
							}
							found = true
						}
						break
					case autov2beta1.PodsMetricSourceType:
						if s1.Pods.MetricName == s2.Pods.MetricName {
							if !reflect.DeepEqual(s1.Pods, s2.Pods) {
								if i == 0 {
									modified = append(modified, s2)
								}
								cMap[string(s1.Type)+"_"+s1.Pods.MetricName] = ""
							}
							found = true
						}
						break
					case autov2beta1.ResourceMetricSourceType:
						if s1.Resource.Name == s2.Resource.Name {
							if !reflect.DeepEqual(s1.Resource, s2.Resource) {
								if i == 0 {
									modified = append(modified, s2)
								}
								cMap[string(s1.Type)+"_"+string(s1.Resource.Name)] = ""
							}
							found = true
						}
						break
					}
				}
			}
			// String not found. We add it to return slice
			if !found {
				if i == 0 {
					deleted = append(deleted, s1)
				} else {
					added = append(added, s1)
				}
			}
		}
		// Swap the slices, only if it was the first loop
		if i == 0 {
			origin, now = now, origin
		}
	}
	return
}
