package operator

import (
	"fmt"
	"github.com/go-logr/logr"
	appv1 "github.com/logancloud/logan-app-operator/pkg/apis/app/v1"
	"github.com/logancloud/logan-app-operator/pkg/logan"
	"github.com/logancloud/logan-app-operator/pkg/logan/config"
	"github.com/logancloud/logan-app-operator/pkg/logan/util"
	"github.com/logancloud/logan-app-operator/pkg/logan/util/keys"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"
)

const (
	defaultAppName               = "app"
	defaultImagePullPolicy       = "Always"
	defaultRevisionHistoryLimits = int(5)
	defaultWeight                = 100

	eventTypeNormal  = "Normal"
	eventTypeWarning = "Warning"
)

// BootHandler is the core struct for handling logic for all boots.
type BootHandler struct {
	OperatorBoot metav1.Object
	OperatorSpec *appv1.BootSpec
	OperatorMeta *metav1.ObjectMeta

	Boot   *appv1.Boot
	Config *config.BootConfig

	Scheme   *runtime.Scheme
	Client   util.K8SClient
	Logger   logr.Logger
	Recorder record.EventRecorder
}

// UpdateAnnotation handle the logic for annotation value, return true if updated
func (handler *BootHandler) UpdateAnnotation(annotationMap map[string]string) bool {
	metaData := handler.OperatorMeta
	updated := false

	if metaData.Annotations == nil {
		metaData.Annotations = make(map[string]string)
	}

	for aKey, aValue := range annotationMap {
		if metaDataVal, exist := metaData.Annotations[aKey]; exist {
			// Annotation Map contains the key
			if metaDataVal != aValue {
				metaData.Annotations[aKey] = aValue
				updated = true
			}
		} else {
			// Annotation Map does not contain the key
			metaData.Annotations[aKey] = aValue
			updated = true
		}
	}

	return updated
}

// NewContainers return the Containers from the boot spec and configmaps
func (handler *BootHandler) NewContainers() []corev1.Container {
	boot := handler.Boot
	bootCfg := handler.Config

	containers := []corev1.Container{*handler.NewAppContainer()}

	sidecarContainers := bootCfg.SidecarContainers

	if sidecarContainers != nil {
		for _, c := range *sidecarContainers {
			sideCarContainer := c.DeepCopy()
			// Replace Envs
			DecodeEnvs(boot, sideCarContainer.Env)

			containers = append(containers, *sideCarContainer)
		}
	}
	return containers
}

// NewPodAnnotations return the pod's Annotations
func (handler *BootHandler) NewPodAnnotations() map[string]string {
	boot := handler.Boot
	annotations := make(map[string]string)
	if boot.Annotations != nil {
		restartAnnotationValue, ok := boot.Annotations[keys.BootRestartedAtAnnotationKey]
		if ok {
			annotations[keys.BootRestartedAtAnnotationKey] = restartAnnotationValue
		}
	}
	return annotations
}

// NewAffinity return the pod's Affinity
func (handler *BootHandler) NewAffinity() *corev1.Affinity {
	boot := handler.Boot

	affinity := &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{
					Weight: defaultWeight,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      keys.BootNameKey,
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{boot.Name},
								},
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		}}

	return affinity
}

// rebuildPodSpec will rebuild the podSpec by boot's PodSpec and the app pvc
func (handler *BootHandler) rebuildPodSpec(podTemplateSpec *corev1.PodTemplateSpec) {
	logger := handler.Logger
	bootCfg := handler.Config
	boot := handler.Boot

	podSpec := bootCfg.AppSpec.PodSpec
	if podSpec != nil {
		appPodSpec := *podSpec.DeepCopy()
		err := util.MergeOverride(&podTemplateSpec.Spec, appPodSpec)
		if err != nil {
			logger.Error(err, "config merge error.", "type", "podSpec")
		}

		initContainers := podTemplateSpec.Spec.InitContainers
		if initContainers != nil && len(initContainers) > 0 {
			for _, c := range initContainers {
				DecodeEnvs(boot, c.Env)
			}
		}
	}

	//add app pvc
	if boot.Spec.Pvc != nil && len(boot.Spec.Pvc) > 0 {
		vols := ConvertVolume(boot.Spec.Pvc)
		if vols != nil {
			if podTemplateSpec.Spec.Volumes == nil {
				podTemplateSpec.Spec.Volumes = make([]corev1.Volume, 0)
			}
			podTemplateSpec.Spec.Volumes = append(podTemplateSpec.Spec.Volumes, vols...)
		}
	}

	// decode
	volumes := podTemplateSpec.Spec.Volumes
	if volumes != nil && len(volumes) > 0 {
		DecodeVolumes(boot, volumes)
	}

}

// NewStatefulSet return a new created Boot's StatefulSet object
func (handler *BootHandler) NewStatefulSet() *appsv1.StatefulSet {
	boot := handler.Boot

	revisionHistoryLimits := int32(defaultRevisionHistoryLimits)

	podLabels := PodLabels(boot)
	workloadLabels := WorkloadLabels(boot)
	containers := handler.NewContainers()
	annotations := handler.NewPodAnnotations()
	affinity := handler.NewAffinity()

	sts := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkloadName(boot),
			Namespace: boot.Namespace,
			Labels:    workloadLabels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:             boot.Spec.Replicas,
			RevisionHistoryLimit: &revisionHistoryLimits,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					Affinity:          affinity,
					Containers:        containers,
					NodeSelector:      boot.Spec.NodeSelector,
					PriorityClassName: boot.Spec.Priority,
				},
			},
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
		},
	}

	partition := int32(0)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type: appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
			Partition: &partition,
		},
	}

	handler.rebuildPodSpec(&sts.Spec.Template)

	_ = controllerutil.SetControllerReference(handler.OperatorBoot, sts, handler.Scheme)

	return sts
}

// NewDeployment return a new created Boot's Deployment object
func (handler *BootHandler) NewDeployment() *appsv1.Deployment {
	boot := handler.Boot

	revisionHistoryLimits := int32(defaultRevisionHistoryLimits)
	podLabels := PodLabels(boot)
	deployLabels := WorkloadLabels(boot)

	containers := handler.NewContainers()
	annotations := handler.NewPodAnnotations()

	affinity := handler.NewAffinity()

	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkloadName(boot),
			Namespace: boot.Namespace,
			Labels:    deployLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             boot.Spec.Replicas,
			RevisionHistoryLimit: &revisionHistoryLimits,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					Affinity:          affinity,
					Containers:        containers,
					NodeSelector:      boot.Spec.NodeSelector,
					PriorityClassName: boot.Spec.Priority,
				},
			},
			Strategy: appsv1.DeploymentStrategy{},
		},
	}

	// Avoid when boot has more than 4 pods, more than one pod will be RollingUpdate.
	if boot.BootType == logan.BootJava {
		maxUnavailable := intstr.FromString("1%")
		dep.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: &maxUnavailable,
			},
		}
	}

	handler.rebuildPodSpec(&dep.Spec.Template)

	_ = controllerutil.SetControllerReference(handler.OperatorBoot, dep, handler.Scheme)

	return dep
}

// NewAppContainer return a new created App Container instance
func (handler *BootHandler) NewAppContainer() *corev1.Container {
	boot := handler.Boot
	imageName := AppContainerImageName(boot, handler.Config.AppSpec)

	appContainer := corev1.Container{
		Image: imageName,
		Name:  defaultAppName,
		Ports: []corev1.ContainerPort{{
			ContainerPort: boot.Spec.Port,
			Name:          HttpPortName,
		}},
		Env:             boot.Spec.Env,
		ImagePullPolicy: defaultImagePullPolicy,
		Resources:       boot.Spec.Resources,
	}

	// If Spec's health is empty string, disable the health check and readiness.
	if boot.Spec.Health != nil && *boot.Spec.Health != "" {
		liveness, readiness := handler.GetHealthProbe()
		appContainer.LivenessProbe = liveness
		appContainer.ReadinessProbe = readiness
	}

	if boot.Spec.Command != nil && len(boot.Spec.Command) > 0 {
		appContainer.Command = boot.Spec.Command
	}

	specContainer := handler.Config.AppSpec.Container
	if specContainer != nil {
		err := util.MergeOverride(&appContainer, *specContainer)
		if err != nil {
			handler.Logger.Error(err, "Merge error.", "type", "container")
		}
	}

	// add pvc
	if boot.Spec.Pvc != nil && len(boot.Spec.Pvc) > 0 {
		if appContainer.VolumeMounts == nil {
			appContainer.VolumeMounts = make([]corev1.VolumeMount, 0)
		}
		vols := ConvertVolumeMount(boot.Spec.Pvc)
		if vols != nil {
			appContainer.VolumeMounts = append(appContainer.VolumeMounts, vols...)
			DecodeVolumeMounts(boot, appContainer.VolumeMounts)
		}
	}

	return &appContainer
}

// GetHealthProbe return the livenessProbe and readinessProbe for the created container
func (handler *BootHandler) GetHealthProbe() (*corev1.Probe, *corev1.Probe) {
	boot := handler.Boot
	healthPort := AppContainerHealthPort(boot, handler.Config.AppSpec)

	// havok issue #95
	failureThreshold := int32(10)
	if boot.BootType == logan.BootPython {
		failureThreshold = int32(15)
	}

	livenessProbe := &corev1.Probe{
		FailureThreshold: failureThreshold,
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   *boot.Spec.Health,
				Port:   healthPort,
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 120,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      5,
	}

	readinessPath := *boot.Spec.Health
	if boot.Spec.Readiness != nil && *boot.Spec.Readiness != "" {
		readinessPath = *boot.Spec.Readiness
	}

	readinessProbe := &corev1.Probe{
		FailureThreshold: failureThreshold,
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   readinessPath,
				Port:   healthPort,
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 60,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      5,
	}

	return livenessProbe, readinessProbe
}

// NewServices returns a new created Service instance
func (handler *BootHandler) NewServices(podSpec *corev1.PodTemplateSpec) []*corev1.Service {
	boot := handler.Boot
	//bootCfg := handler.Config
	// app Service
	prometheusScrape := allowPrometheusScrape(boot, handler.Config.AppSpec)
	bootSvc := handler.createService(int(boot.Spec.Port), boot.Name, prometheusScrape, corev1.ServiceTypeClusterIP)
	allSvcs := []*corev1.Service{bootSvc}

	// only dev environment and nodePort true, create nodePort service
	if handler.Boot.Spec.NodePort == "true" && logan.OperDev == "dev" {
		svcName := NodePortServiceName(boot)
		allSvcs = append(allSvcs, handler.createService(int(boot.Spec.Port), svcName, false, corev1.ServiceTypeNodePort))
	}

	// additional sidecar Service
	if len(podSpec.Spec.Containers) > 1 {
		sidecarContainers := podSpec.Spec.Containers[1:]
		for _, sidecarContainer := range sidecarContainers {
			if sidecarContainer.Ports != nil {
				for _, port := range sidecarContainer.Ports {
					svcName := SideCarServiceName(boot, port)
					allSvcs = append(allSvcs, handler.createService(int(port.ContainerPort), svcName, true, corev1.ServiceTypeClusterIP))
				}
			}
		}
	}

	return allSvcs
}

// createService returns a new created Service instance
func (handler *BootHandler) createService(port int, name string, prometheusScrape bool, serviceType corev1.ServiceType) *corev1.Service {
	boot := handler.Boot

	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   boot.Namespace,
			Labels:      ServiceLabels(boot),
			Annotations: ServiceAnnotation(prometheusScrape, port),
		},
	}

	serviceSpec := corev1.ServiceSpec{
		Ports: []corev1.ServicePort{
			{
				Name:       HttpPortName,
				Port:       int32(port),
				TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: int32(port)},
			},
		},
		Selector: PodLabels(boot),
		Type:     serviceType,
	}

	if boot.Spec.SessionAffinity != "" {
		serviceSpec.SessionAffinity = corev1.ServiceAffinity(boot.Spec.SessionAffinity)
	} else {
		serviceSpec.SessionAffinity = corev1.ServiceAffinityNone
	}

	svc.Spec = serviceSpec

	// Set Boot instance as the owner and controller
	_ = controllerutil.SetControllerReference(handler.OperatorBoot, svc, handler.Scheme)

	return svc
}

func getEventType(reason string, err error) string {
	if err == nil && !strings.Contains(reason, "Failed") {
		return eventTypeNormal
	}

	// following failed type can auto fix by reconcile loop
	if reason == keys.FailedUpdateBootDefaulters || reason == keys.FailedUpdateBootMeta ||
		reason == keys.FailedGetDeployment || reason == keys.FailedGetService {
		return eventTypeNormal
	}

	// following error type can auto fix by reconcile loop
	if errors.IsConflict(err) || errors.IsAlreadyExists(err) ||
		errors.IsServiceUnavailable(err) || errors.IsServerTimeout(err) ||
		errors.IsInternalError(err) || errors.IsTimeout(err) {
		return eventTypeNormal
	}

	return eventTypeWarning
}

// RecordEvent will record the event string by error type
func (handler *BootHandler) RecordEvent(reason string, message string, err error) {
	recorder := handler.Recorder
	boot := handler.Boot
	eventType := getEventType(reason, err)

	if err != nil {
		message = message + fmt.Sprintf(", error: %s", err.Error())
	}

	recorder.Event(boot, eventType, reason, message)
}
