package ingressoperator

import (
	"fmt"
	hyperv1 "github.com/openshift/hypershift/api/v1alpha1"
	"github.com/openshift/hypershift/control-plane-operator/controllers/hostedcontrolplane/kas"
	"github.com/openshift/hypershift/control-plane-operator/controllers/hostedcontrolplane/manifests"
	"github.com/openshift/hypershift/support/config"
	"github.com/openshift/hypershift/support/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilpointer "k8s.io/utils/pointer"
)

const (
	ingressOperatorContainerName = "ingress-operator"
	ingressOperatorMetricsPort   = 60000
)

type Params struct {
	IngressOperatorImage    string
	HAProxyRouterImage      string
	KubeRBACProxyImage      string
	ReleaseVersion          string
	TokenMinterImage        string
	AvailabilityProberImage string
	DeploymentConfig        config.DeploymentConfig
}

func NewParams(hcp *hyperv1.HostedControlPlane, version string, images map[string]string, setDefaultSecurityContext bool) Params {
	p := Params{
		IngressOperatorImage:    images["cluster-ingress-operator"],
		HAProxyRouterImage:      images["haproxy-router"],
		ReleaseVersion:          version,
		TokenMinterImage:        images["token-minter"],
		AvailabilityProberImage: images[util.AvailabilityProberImageName],
	}
	p.DeploymentConfig.Scheduling.PriorityClass = config.DefaultPriorityClass
	p.DeploymentConfig.SetColocation(hcp)
	p.DeploymentConfig.SetRestartAnnotation(hcp.ObjectMeta)
	p.DeploymentConfig.SetControlPlaneIsolation(hcp)
	p.DeploymentConfig.Replicas = 1
	p.DeploymentConfig.SetDefaultSecurityContext = setDefaultSecurityContext
	p.DeploymentConfig.ReadinessProbes = config.ReadinessProbes{
		ingressOperatorContainerName: {
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/metrics",
					Port:   intstr.FromInt(ingressOperatorMetricsPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       60,
			SuccessThreshold:    1,
			FailureThreshold:    3,
			TimeoutSeconds:      5,
		},
	}
	p.DeploymentConfig.LivenessProbes = config.LivenessProbes{
		ingressOperatorContainerName: {
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/metrics",
					Port:   intstr.FromInt(ingressOperatorMetricsPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 60,
			PeriodSeconds:       60,
			SuccessThreshold:    1,
			FailureThreshold:    5,
			TimeoutSeconds:      5,
		},
	}

	return p
}

func ReconcileDeployment(dep *appsv1.Deployment, params Params, apiPort *int32) {
	dep.Spec.Replicas = utilpointer.Int32(1)
	dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"name": "ingress-operator"}}
	dep.Spec.Strategy.Type = appsv1.RecreateDeploymentStrategyType
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["target.workload.openshift.io/management"] = `{"effect": "PreferredDuringScheduling"}`
	if dep.Spec.Template.Labels == nil {
		dep.Spec.Template.Labels = map[string]string{}
	}
	dep.Spec.Template.Labels["name"] = "ingress-operator"
	dep.Spec.Template.Spec.AutomountServiceAccountToken = utilpointer.BoolPtr(false)
	dep.Spec.Template.Spec.Containers = []corev1.Container{
		{
			Command: []string{
				"ingress-operator",
				"start",
				"--namespace",
				"openshift-ingress-operator",
				"--image",
				"$(IMAGE)",
				"--canary-image",
				"$(CANARY_IMAGE)",
				"--release-version",
				"$(RELEASE_VERSION)",
				"--metrics-listen-addr",
				fmt.Sprintf("0.0.0.0:%d", ingressOperatorMetricsPort),
			},
			Env: []corev1.EnvVar{
				{Name: "RELEASE_VERSION", Value: params.ReleaseVersion},
				{Name: "IMAGE", Value: params.HAProxyRouterImage},
				{Name: "CANARY_IMAGE", Value: params.IngressOperatorImage},
				{Name: "KUBECONFIG", Value: "/etc/kubernetes/kubeconfig"},
			},
			Name:            ingressOperatorContainerName,
			Image:           params.IngressOperatorImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("56Mi"),
			}},
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "ingress-operator-kubeconfig", MountPath: "/etc/kubernetes"},
				{Name: "serviceaccount-token", MountPath: "/var/run/secrets/openshift/serviceaccount"},
			},
		},
		{
			Name:    "token-minter",
			Command: []string{"/usr/bin/token-minter"},
			Args: []string{
				"-service-account-namespace=openshift-ingress-operator",
				"-service-account-name=ingress-operator",
				"-token-file=/var/run/secrets/openshift/serviceaccount/token",
				"-kubeconfig=/etc/kubernetes/kubeconfig",
			},
			Image: params.TokenMinterImage,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "serviceaccount-token", MountPath: "/var/run/secrets/openshift/serviceaccount"},
				{Name: "admin-kubeconfig", MountPath: "/etc/kubernetes"},
			},
		},
	}

	dep.Spec.Template.Spec.Volumes = []corev1.Volume{
		{Name: "serviceaccount-token", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "admin-kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "service-network-admin-kubeconfig"}}},
		{Name: "ingress-operator-kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: manifests.IngressOperatorKubeconfig("").Name}}},
	}

	util.AvailabilityProber(
		kas.InClusterKASReadyURL(dep.Namespace, apiPort),
		params.AvailabilityProberImage,
		&dep.Spec.Template.Spec,
		func(o *util.AvailabilityProberOpts) {
			o.KubeconfigVolumeName = "ingress-operator-kubeconfig"
			o.RequiredAPIs = []schema.GroupVersionKind{
				{Group: "route.openshift.io", Version: "v1", Kind: "Route"},
			}
		},
	)

	params.DeploymentConfig.ApplyTo(dep)
}