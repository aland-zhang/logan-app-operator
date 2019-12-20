package validation

import (
	"context"
	"fmt"
	"github.com/logancloud/logan-app-operator/pkg/logan"
	"github.com/logancloud/logan-app-operator/pkg/logan/util/keys"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"

	appv1 "github.com/logancloud/logan-app-operator/pkg/apis/app/v1"
	v1 "github.com/logancloud/logan-app-operator/pkg/apis/app/v1"
	"github.com/logancloud/logan-app-operator/pkg/controller/javaboot"
	"github.com/logancloud/logan-app-operator/pkg/controller/nodejsboot"
	"github.com/logancloud/logan-app-operator/pkg/controller/phpboot"
	"github.com/logancloud/logan-app-operator/pkg/controller/pythonboot"
	"github.com/logancloud/logan-app-operator/pkg/controller/webboot"
	"github.com/logancloud/logan-app-operator/pkg/logan/config"
	"github.com/logancloud/logan-app-operator/pkg/logan/operator"
	"github.com/logancloud/logan-app-operator/pkg/logan/util"
	"github.com/logancloud/logan-app-operator/pkg/logan/webhook"
	admssionv1beta1 "k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	scheduling "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"net/http"
	"reflect"
	"regexp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/runtime/inject"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sort"
	"strconv"
	"strings"
)

const (
	dns1123Label = "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
)

// BootValidator is a Handler that implements interfaces: admission.Handler, inject.Client and inject.Decoder
type BootValidator struct {
	client   util.K8SClient
	decoder  *admission.Decoder
	Schema   *runtime.Scheme
	Recorder record.EventRecorder
}

var _ admission.Handler = &BootValidator{}

// Handle is the actual logic that will be called by every webhook request
func (vHandler *BootValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if operator.Ignore(req.AdmissionRequest.Namespace) {
		return admission.ValidationResponse(true, "")
	}

	msg, valid, err := vHandler.Validate(req)
	if err != nil {
		logger.Error(err, msg)
		return admission.Errored(http.StatusBadRequest, err)
	}

	if !valid {
		return admission.ValidationResponse(false, msg)
	}

	return admission.ValidationResponse(true, "")
}

var _ inject.Client = &BootValidator{}

// InjectClient will inject client into BootValidator
func (vHandler *BootValidator) InjectClient(c client.Client) error {
	vHandler.client = util.NewClient(c)
	return nil
}

var _ admission.DecoderInjector = &BootValidator{}

// InjectDecoder will inject decoder into BootValidator
func (vHandler *BootValidator) InjectDecoder(d *admission.Decoder) error {
	vHandler.decoder = d
	return nil
}

// Validate will do the validating for request boot.
// Returns
//   msg: Error message
//   valid: true if valid, otherwise false
//   error: decoding error, otherwise nil
func (vHandler *BootValidator) Validate(req admission.Request) (string, bool, error) {
	operation := req.AdmissionRequest.Operation

	boot, err := webhook.DecodeBoot(req, vHandler.decoder)
	if err != nil {
		return "Decoding request error", false, err
	}

	if boot == nil {
		logger.Info("Can not recognize the bootType", "bootType", req.AdmissionRequest.Kind.Kind)
		return "Can not decoding boot", false, nil
	}

	// Only Check Boot's names when creating.
	if operation == admssionv1beta1.Create {
		msg, valid := vHandler.BootNameExist(boot)

		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}
	}

	// Check Boot's envs when creating or updating.
	// Check Boot's pvc when creating or updating.
	// Check Boot's priority when creating or updating.
	// Record a revision when creating or updating if validation Boot valid.
	if operation == admssionv1beta1.Create || operation == admssionv1beta1.Update {
		msg, valid := vHandler.CheckEnvKeys(boot, operation)

		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}

		msg, valid = vHandler.validateHpa(boot, operation)
		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}

		msg, valid = vHandler.validateWorkload(boot, operation)
		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}

		msg, valid = vHandler.checkPriority(boot, operation)
		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}

		msg, valid = vHandler.CheckPvc(boot, operation)
		if !valid {
			logger.Info(msg)
			return msg, false, nil
		}

		flag, err := vHandler.recordRevision(boot, req)
		if err != nil || flag == false {
			return "create up revision error", flag, err
		}
	}

	//will never execute if admssionv1beta1.Delete not set on webhook
	if operation == admssionv1beta1.Delete {
		_, err := vHandler.deleteRevision(boot)
		if err != nil {
			return "remove all revision error", false, err
		}
	}

	logger.Info("Validation Boot valid: ",
		"name", boot.Name, "namespace", boot.Namespace, "operation", operation)

	return "", true, nil
}

// recordRevision will make a new revision record on boot created or update
func (vHandler *BootValidator) recordRevision(inputBoot *v1.Boot, req admission.Request) (bool, error) {
	// if false,do not record a revision
	//if !logan.MutationDefaulter {
	//	return true, nil
	//}

	// merge boot's default value from operator's configmap, before record a revision
	spec, meta := vHandler.mergeBootDefaultValue(inputBoot, req)
	if spec == nil || meta == nil {
		return false, nil
	}
	boot := inputBoot.DeepCopy()
	boot.Spec = *spec
	boot.ObjectMeta = *meta

	// init a revision
	revisionBoot := operator.InitBootRevision(boot)
	hashcode := revisionBoot.BootHash()
	logger.V(1).Info("RevisionBoot's BootHash", "BootHash", hashcode, "revision", revisionBoot)
	revisionBoot.Annotations[keys.BootRevisionHashAnnotationKey] = hashcode

	// get ListRevision
	c := vHandler.client
	bootLabels := operator.PodLabels(boot)
	revisionList, err := c.ListRevision(boot.Namespace, bootLabels)
	if err != nil {
		return false, err
	}

	// the first revision
	if len(revisionList.Items) == 0 {
		revisionBoot.Annotations[keys.BootRevisionIdAnnotationKey] = "1"
		revisionBoot.Annotations[keys.BootRevisionPhaseAnnotationKey] = operator.RevisionPhaseRunning
		revisionBoot.Annotations[keys.BootRevisionDiffAnnotationKey] = ""
		revisionBoot.Annotations[keys.BootRevisionRetryAnnotationKey] = "0"
		revisionBoot.Name = revisionBoot.Name + "-" + revisionBoot.Annotations[keys.BootRevisionIdAnnotationKey]
		revisionBoot.Labels = bootLabels

		logger.Info("Create a new revision", "revision", revisionBoot)
		err = c.Create(context.TODO(), revisionBoot)
		if err != nil {
			logger.Error(err, "Can not create revision", "revision", revisionBoot)
			return false, err
		}

		return true, nil
	}

	// Compared to the previous revision
	latestRevision := revisionList.SelectLatestRevision()
	latestHash := latestRevision.Annotations[keys.BootRevisionHashAnnotationKey]
	logger.V(1).Info("The latest revisionBoot's BootHash", "BootHash", latestHash, "revision", latestRevision)

	// something change
	if latestHash != hashcode {
		// Add a new revision to history
		latestRevisionId := latestRevision.GetRevisionId()
		newRevisionId := latestRevisionId + 1
		revisionBoot.Annotations[keys.BootRevisionIdAnnotationKey] = strconv.Itoa(newRevisionId)
		revisionBoot.Annotations[keys.BootRevisionPhaseAnnotationKey] = operator.RevisionPhaseRunning
		revisionBoot.Annotations[keys.BootRevisionDiffAnnotationKey] = operator.RevisionDiff(*revisionBoot, *latestRevision)
		revisionBoot.Annotations[keys.BootRevisionRetryAnnotationKey] = "0"
		revisionBoot.Name = revisionBoot.Name + "-" + revisionBoot.Annotations[keys.BootRevisionIdAnnotationKey]
		revisionBoot.Labels = bootLabels

		logger.Info("Add a new revision to history", "revision", revisionBoot)
		err = c.Create(context.TODO(), revisionBoot)
		if err != nil {
			logger.Error(err, "Can not create revision", "revision", revisionBoot)
			return false, err
		}

		// Update the previous revision's phase
		latestPhase := latestRevision.Annotations[keys.BootRevisionPhaseAnnotationKey]
		newPhase := latestPhase
		if latestPhase == operator.RevisionPhaseRunning {
			newPhase = operator.RevisionPhaseCancel
		} else if latestPhase == operator.RevisionPhaseActive {
			newPhase = operator.RevisionPhaseComplete
		}
		latestRevision.Annotations[keys.BootRevisionPhaseAnnotationKey] = newPhase
		logger.Info("Update the previous revision's phase", "revision", latestRevision, "from", latestPhase, "to", newPhase)
		err = c.Update(context.TODO(), latestRevision)
		if err != nil {
			logger.Error(err, "Can not update the previous revision's phase", "revision", latestRevision)
			return false, err
		}

		// keep max history revision
		return vHandler.keepRevisionMaxSizeLimit(revisionList, logan.MaxHistory-1)
	}

	//maybe just scale or redeploy
	logger.V(1).Info("No need to do revision with boot", "revision", revisionBoot)
	return true, nil

}

// mergeBootDefaultValue will merge boot config with operator app config
func (vHandler *BootValidator) mergeBootDefaultValue(boot *v1.Boot, req admission.Request) (*appv1.BootSpec, *metav1.ObjectMeta) {
	appType := req.AdmissionRequest.Kind.Kind
	if appType == webhook.ApiTypeJava {
		javaBoot := boot.DeepCopyJava()
		handler := javaboot.InitHandler(javaBoot, vHandler.Schema, vHandler.client, logger, vHandler.Recorder)
		handler.DefaultValue()
		return &javaBoot.Spec, &javaBoot.ObjectMeta
	} else if appType == webhook.ApiTypePython {
		pythonBoot := boot.DeepCopyPython()
		handler := pythonboot.InitHandler(pythonBoot, vHandler.Schema, vHandler.client, logger, vHandler.Recorder)
		handler.DefaultValue()
		return &pythonBoot.Spec, &pythonBoot.ObjectMeta
	} else if appType == webhook.ApiTypePhp {
		phpBoot := boot.DeepCopyPhp()
		handler := phpboot.InitHandler(phpBoot, vHandler.Schema, vHandler.client, logger, vHandler.Recorder)
		handler.DefaultValue()
		return &phpBoot.Spec, &phpBoot.ObjectMeta
	} else if appType == webhook.ApiTypeNodeJS {
		nodejsBoot := boot.DeepCopyNodeJS()
		handler := nodejsboot.InitHandler(nodejsBoot, vHandler.Schema, vHandler.client, logger, vHandler.Recorder)
		handler.DefaultValue()
		return &nodejsBoot.Spec, &nodejsBoot.ObjectMeta
	} else if appType == webhook.ApiTypeWeb {
		webBoot := boot.DeepCopyWeb()
		handler := webboot.InitHandler(webBoot, vHandler.Schema, vHandler.client, logger, vHandler.Recorder)
		handler.DefaultValue()
		return &webBoot.Spec, &webBoot.ObjectMeta
	}

	return nil, nil
}

// keepRevisionMaxSizeLimit will keep only the Max size revisions
func (vHandler *BootValidator) keepRevisionMaxSizeLimit(lst *v1.BootRevisionList, size int) (bool, error) {
	if len(lst.Items) > size {
		items := lst.Items
		sort.Slice(items, func(i, j int) bool {
			aId := (&items[i]).GetRevisionId()
			bId := (&items[j]).GetRevisionId()
			return aId < bId
		})

		delSize := len(items) - size
		delLst := items[:delSize]

		c := vHandler.client

		for _, revision := range delLst {
			logger.V(1).Info("Delete history revision.", "revision", revision)
			err := c.Delete(context.TODO(), revision.DeepCopyObject())
			if err != nil {
				logger.Error(err, "Delete history revision error.", "revision", revision)
				return false, err
			}
		}
	}

	return true, nil
}

//deleteRevision will delete all revision if boot is delete
func (vHandler *BootValidator) deleteRevision(boot *v1.Boot) (bool, error) {
	c := vHandler.client
	bootLabels := operator.PodLabels(boot)
	revisionList, err := c.ListRevision(boot.Namespace, bootLabels)
	if err != nil {
		return false, err
	}
	for _, revision := range revisionList.Items {
		err := c.Delete(context.TODO(), revision.DeepCopyObject())
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

// BootNameExist check if name is exist.
// Returns
//    msg: error message
//    valid: If exist false, otherwise false
func (vHandler *BootValidator) BootNameExist(boot *v1.Boot) (string, bool) {
	c := vHandler.client

	namespaceName := k8stypes.NamespacedName{
		Namespace: boot.Namespace,
		Name:      boot.Name,
	}

	err := c.Get(context.TODO(), namespaceName, &appv1.JavaBoot{})
	if err == nil {
		return fmt.Sprintf("Boot's name %s exists in type %s", namespaceName, webhook.ApiTypeJava), false
	}

	err = c.Get(context.TODO(), namespaceName, &appv1.PhpBoot{})
	if err == nil {
		return fmt.Sprintf("Boot's name %s exists in type %s", namespaceName, webhook.ApiTypePhp), false
	}

	err = c.Get(context.TODO(), namespaceName, &appv1.PythonBoot{})
	if err == nil {
		return fmt.Sprintf("Boot's name %s exists in type %s", namespaceName, webhook.ApiTypePython), false
	}

	err = c.Get(context.TODO(), namespaceName, &appv1.NodeJSBoot{})
	if err == nil {
		return fmt.Sprintf("Boot's name %s exists in type %s", namespaceName, webhook.ApiTypeNodeJS), false
	}

	err = c.Get(context.TODO(), namespaceName, &appv1.WebBoot{})
	if err == nil {
		return fmt.Sprintf("Boot's name %s exists in type %s", namespaceName, webhook.ApiTypeWeb), false
	}

	return "", true
}

func (vHandler *BootValidator) getLatest(boot *v1.Boot) (*v1.Boot, error) {
	c := vHandler.client

	namespaceName := k8stypes.NamespacedName{
		Namespace: boot.Namespace,
		Name:      boot.Name,
	}

	kind := boot.BootType
	switch kind {
	case logan.BootJava:
		rawBoot := &appv1.JavaBoot{}
		err := c.Get(context.TODO(), namespaceName, rawBoot)
		if err != nil {
			return nil, err
		}
		return rawBoot.DeepCopyBoot(), nil
	case logan.BootPython:
		rawBoot := &appv1.PythonBoot{}
		err := c.Get(context.TODO(), namespaceName, rawBoot)
		if err != nil {
			return nil, err
		}
		return rawBoot.DeepCopyBoot(), nil
	case logan.BootPhp:
		rawBoot := &appv1.PhpBoot{}
		err := c.Get(context.TODO(), namespaceName, rawBoot)
		if err != nil {
			return nil, err
		}
		return rawBoot.DeepCopyBoot(), nil
	case logan.BootNodeJS:
		rawBoot := &appv1.NodeJSBoot{}
		err := c.Get(context.TODO(), namespaceName, rawBoot)
		if err != nil {
			return nil, err
		}
		return rawBoot.DeepCopyBoot(), nil
	case logan.BootWeb:
		rawBoot := &appv1.WebBoot{}
		err := c.Get(context.TODO(), namespaceName, rawBoot)
		if err != nil {
			return nil, err
		}
		return rawBoot.DeepCopyBoot(), nil
	default:
		return nil, unknowBoot(kind)
	}
}

func unknowBoot(kind string) error {
	return &errors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure,
		Code:   http.StatusNotFound,
		Reason: metav1.StatusReasonNotFound,
		Details: &metav1.StatusDetails{
			Name: kind,
		},
		Message: fmt.Sprintf("BootType %s not found", kind),
	}}
}

// CheckPvc check the boot's pvc, pvc should exist and the label match the boot.
// Returns
//    msg: error message
//    valid: If valid false, otherwise false
func (vHandler *BootValidator) CheckPvc(boot *v1.Boot, operation admssionv1beta1.Operation) (string, bool) {
	if boot.Spec.Pvc == nil {
		return "", true
	}

	for _, pvc := range boot.Spec.Pvc {
		valid, err := vHandler.validatePvc(boot, pvc)
		if !valid {
			return err, false
		}

		pvcName, _ := operator.Decode(boot, pvc.Name)
		_, shared, owner, err := vHandler.checkPvcOwner(boot, pvc)
		if err != "" {
			return err, false
		}

		if !shared && !owner {
			return fmt.Sprintf("the pvc %s's label don't match the boot %s. the pvc %s also is not a shared pvc.",
				pvcName, boot.Name, pvcName), false
		}
	}

	return "", true
}

func (vHandler *BootValidator) validateHpa(boot *appv1.Boot, operation admssionv1beta1.Operation) (string, bool) {
	if boot.Spec.Hpa != nil {

		if boot.Spec.Hpa.MaxReplicas == nil && boot.Spec.Hpa.MinReplicas != nil {
			return fmt.Sprintf("If MinReplicas is defined, the boot's HPA MaxReplicas can not be nil."), false
		}

		if boot.Spec.Hpa.MaxReplicas != nil && boot.Spec.Hpa.MinReplicas != nil {
			if *boot.Spec.Hpa.MaxReplicas < *boot.Spec.Hpa.MinReplicas {
				return fmt.Sprintf("The boot's HPA MaxReplicas must be greater than or equal to MinReplicas"), false
			}
		}
		hpaField := field.NewPath("hpa")
		errLst := util.ValidateMetrics(boot.Spec.Hpa.Metrics, hpaField.Child("metrics"))
		if len(errLst) > 0 {
			return fmt.Sprintf("Boot's HPA Metrics validation fails: %s", errLst), false
		}
	}
	return "", true
}

func (vHandler *BootValidator) validateWorkload(boot *appv1.Boot, operation admssionv1beta1.Operation) (string, bool) {
	if operation == admssionv1beta1.Update {
		if boot.Status.Workload != "" {
			workload := appv1.Deployment
			if boot.Spec.Workload != "" {
				workload = boot.Spec.Workload
			}

			if boot.Status.Workload != workload {
				return fmt.Sprintf("The boot %s's workload is a immutable field.Can not change from %s to %s.",
					boot.Name, boot.Status.Workload, boot.Spec.Workload), false
			}
			return "", true
		}
	}
	return "", true
}

// validatePvc will validate the pvcName, mountPath
func (vHandler *BootValidator) validatePvc(boot *appv1.Boot, pvcMount appv1.PersistentVolumeClaimMount) (bool, string) {
	pvcName, _ := operator.Decode(boot, pvcMount.Name)
	if len(pvcName) == 0 || len(pvcName) > 63 {
		return false, fmt.Sprintf("the pvc name %s must be not empty and no more than 63 characters", pvcName)
	}

	if isOk, _ := regexp.MatchString(dns1123Label, pvcName); !isOk {
		return false, fmt.Sprintf("the pvc %s is a DNS-1123 label. "+
			"a DNS-1123 label must consist of lower case alphanumeric characters, '-' or '.', "+
			"and must start and end with an alphanumeric character "+
			"(e.g. 'my-name',  or '123-abc', regex used for validation is '%s')",
			pvcName, dns1123Label)
	}

	if len(pvcMount.MountPath) == 0 || strings.Index(pvcMount.MountPath, ":") >= 0 {
		return false, "the pvc MountPath must be not empty and  not contain ':'"
	}

	return true, ""
}

// checkPvcOwner will check the pvc is shared or match the label
func (vHandler *BootValidator) checkPvcOwner(boot *appv1.Boot, pvcMount appv1.PersistentVolumeClaimMount) (bool, bool, bool, string) {
	c := vHandler.client

	pvc := &corev1.PersistentVolumeClaim{}

	pvcName, _ := operator.Decode(boot, pvcMount.Name)

	err := c.Get(context.TODO(),
		k8stypes.NamespacedName{
			Namespace: boot.Namespace,
			Name:      pvcName,
		}, pvc)

	if err != nil && errors.IsNotFound(err) {
		return false, false, false, fmt.Sprintf("the pvc %s don't exist in namespace %s.",
			pvcName, boot.Namespace)
	}

	if pvc.Labels != nil {
		shared, found := pvc.Labels[keys.SharedKey]
		if found {
			if "true" == shared {
				if pvcMount.ReadOnly == true {
					return true, true, false, ""
				}
				return true, true, false,
					fmt.Sprintf("the pvc %s is a shared pvc, should be readOnly", pvcName)

			}
		}

		podLabels := operator.PodLabels(boot)
		if reflect.DeepEqual(podLabels, pvc.Labels) {
			return true, false, true, ""
		}
	}
	return true, false, false, ""
}

func (vHandler *BootValidator) checkPriority(boot *v1.Boot, operation admssionv1beta1.Operation) (string, bool) {
	if len(boot.Spec.Priority) != 0 {
		c := vHandler.client
		pc := &scheduling.PriorityClass{}
		err := c.Get(context.TODO(), k8stypes.NamespacedName{
			Name: boot.Spec.Priority,
		}, pc)

		if err != nil && errors.IsNotFound(err) {
			return fmt.Sprintf("the PriorityClass %s  don't exist in  cluster.",
				boot.Spec.Priority), false
		}

		if !util.PriorityClassPermittedInNamespace(boot.Spec.Priority, boot.Namespace) {
			return fmt.Sprintf("namespace %s can not use  PriorityClass %s.",
				boot.Namespace, boot.Spec.Priority), false
		}

		if pc.Annotations == nil {
			return fmt.Sprintf("namespace %s can not use  PriorityClass %s.",
				boot.Namespace, boot.Spec.Priority), false
		}
		_, found := pc.Annotations[keys.BootPriorityAnnotaionKeyPrefix+boot.Namespace]
		if !found {
			return fmt.Sprintf("namespace %s can not use  PriorityClass %s.",
				boot.Namespace, boot.Spec.Priority), false
		}
	}

	return "", true
}

// CheckEnvKeys check the boot's env keys.
// Returns
//    msg: error message
//    valid: If valid false, otherwise false
func (vHandler *BootValidator) CheckEnvKeys(boot *v1.Boot, operation admssionv1beta1.Operation) (string, bool) {
	configSpec := operator.GetConfigSpec(boot)
	if configSpec == nil {
		logger.Info("AppSpec is nil, valid is true.")
		return "", true
	}

	specField := field.NewPath("spec")
	errLst := util.ValidateEnv(boot.Spec.Env, specField.Child("env"))
	if len(errLst) > 0 {
		return fmt.Sprintf("Boot's Env validation fails: %s", errLst), false
	}

	msg, ret := vHandler.checkSecret(boot)
	if !ret {
		return msg, ret
	}

	//Creating: should not contains the key in global settings.
	if operation == admssionv1beta1.Create {
		for _, cfgEnv := range configSpec.Env {
			cfgEnvName := cfgEnv.Name
			// Decode the ${APP}, ${ENV} context
			cfgEnvValue, _ := operator.Decode(boot, cfgEnv.Value)

			tmpCfgEnv := corev1.EnvVar{
				Name:      cfgEnvName,
				Value:     cfgEnvValue,
				ValueFrom: cfgEnv.ValueFrom,
			}

			for _, env := range boot.Spec.Env {
				if env.Name == cfgEnvName {

					envVal, _ := operator.Decode(boot, env.Value)

					bootEnv := corev1.EnvVar{
						Name:      env.Name,
						Value:     envVal,
						ValueFrom: env.ValueFrom,
					}

					if !reflect.DeepEqual(bootEnv, tmpCfgEnv) {
						return fmt.Sprintf("Boot's added Env [%s=%s] not allowed with settings [%s=%s]",
							env.Name, bootEnv, cfgEnvName, tmpCfgEnv), false
					}
				}
			}
		}

		return "", true
	}

	if operation == admssionv1beta1.Update {
		return vHandler.checkEnvUpdate(configSpec, boot)
	}

	return "", true
}

func (vHandler *BootValidator) checkSecret(boot *appv1.Boot) (string, bool) {
	c := vHandler.client
	for _, env := range boot.Spec.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			name := env.ValueFrom.SecretKeyRef.Name
			key := env.ValueFrom.SecretKeyRef.Key

			secret := &corev1.Secret{}

			namespaceName := k8stypes.NamespacedName{
				Namespace: boot.Namespace,
				Name:      name,
			}

			err := c.Get(context.TODO(), namespaceName, secret)
			if errors.IsNotFound(err) {
				return fmt.Sprintf("Can not found secret: %s", name), false
			}

			_, ok := secret.Data[key]
			if !ok {
				return fmt.Sprintf("Can not found key:%s in secret: %s", key, name), false
			}

			if secret.Annotations == nil {
				return fmt.Sprintf("Boot %s's permission for secret %s isn't granted", boot.Name, name), false
			}

			_, ok = secret.Annotations[keys.BootSecretAnnotaionKeyPrefix+boot.Name]
			if !ok {
				return fmt.Sprintf("Boot %s's permission for secret %s isn't granted", boot.Name, name), false
			}
		}
	}

	return "", true
}

func (vHandler *BootValidator) getMetaEnvs(boot *v1.Boot) ([]corev1.EnvVar, string, error) {
	bootMetaEnvs, err := operator.DecodeAnnotationEnvs(boot)
	if err != nil {
		logger.Error(err, "Boot's annotation env decode error")
		return nil, "Boot's annotation env decode error", err
	}

	if bootMetaEnvs == nil {
		logger.Info("Boot's annotation env decode empty",
			"namespace", boot.Namespace, "name", boot.Name)

		latestBoot, err := vHandler.getLatest(boot)
		if err != nil {
			logger.Error(err, "can not find latest Boot", "boot", boot)
			return nil, fmt.Sprintf("can not find latest Boot for %s", boot.Name), err
		}
		bootMetaEnvs, err := operator.DecodeAnnotationEnvs(latestBoot)
		if err != nil {
			logger.Error(err, "Boot's annotation env decode error")
			return nil, "Boot's annotation env decode error", err
		}
		if bootMetaEnvs == nil {
			logger.Info("lastest Boot's annotation env decode empty",
				"namespace", boot.Namespace, "name", boot.Name)
			return nil, "", nil
		}
		logger.Info("lastest Boot's annotation env decode isn't empty")
	}
	return bootMetaEnvs, "", nil
}

// checkEnvUpdate will check the envs is update
func (vHandler *BootValidator) checkEnvUpdate(configSpec *config.AppSpec, boot *v1.Boot) (string, bool) {
	bootMetaEnvs, msg, err := vHandler.getMetaEnvs(boot)
	if err != nil {
		return msg, false
	}

	if bootMetaEnvs == nil {
		return "", true
	}

	deleted, added, modified := util.Difference2(bootMetaEnvs, boot.Spec.Env)

	logger.V(1).Info("Validating Boot", "deleted", deleted,
		"added", added, "modified", modified)

	for _, cfgEnv := range configSpec.Env {
		cfgEnvName := cfgEnv.Name
		// Decode the ${APP}, ${ENV} context
		cfgEnvValue, _ := operator.Decode(boot, cfgEnv.Value)

		tmpCfgEnv := corev1.EnvVar{
			Name:      cfgEnvName,
			Value:     cfgEnv.Value,
			ValueFrom: cfgEnv.ValueFrom,
		}

		// 1. Manual Delete key of Env: If key exists in global settings, valid is false.
		for _, env := range deleted {
			if env.Name == cfgEnvName {
				return fmt.Sprintf("Boot's deleted Env [%s=%s] not allowed with settings [%s=%s]",
					env.Name, env.Value, cfgEnvName, cfgEnvValue), false
			}
		}

		// 2. Manual Add key of Env: If key exists in global settings, and value not equal, valid is false.
		for _, env := range added {
			if env.Name == cfgEnvName {
				envVal, _ := operator.Decode(boot, env.Value)

				bootEnv := corev1.EnvVar{
					Name:      env.Name,
					Value:     envVal,
					ValueFrom: env.ValueFrom,
				}

				if !reflect.DeepEqual(tmpCfgEnv, bootEnv) {
					return fmt.Sprintf("Boot's added Env [%s=%s] not allowed with settings [%s=%s]",
						env.Name, env.Value, cfgEnvName, cfgEnvValue), false
				}
			}
		}

		// 3. Manual Modify value of Env: If key exists in global settings, and value not equal, valid is false.
		for _, env := range modified {
			if env.Name == cfgEnvName {
				envVal, _ := operator.Decode(boot, env.Value)

				bootEnv := corev1.EnvVar{
					Name:      env.Name,
					Value:     envVal,
					ValueFrom: env.ValueFrom,
				}
				if !reflect.DeepEqual(tmpCfgEnv, bootEnv) {
					return fmt.Sprintf("Boot's edit Env [%s=%s] not allowed with settings [%s=%s]",
						env.Name, env.Value, cfgEnvName, cfgEnvValue), false
				}
			}
		}
	}

	return "", true
}
