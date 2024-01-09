// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/amazon-cloudwatch-agent-operator/apis/v1alpha1"
	"github.com/aws/amazon-cloudwatch-agent-operator/internal/manifests"
	"github.com/aws/amazon-cloudwatch-agent-operator/internal/manifests/collector/adapters"
	"github.com/aws/amazon-cloudwatch-agent-operator/internal/manifests/manifestutils"
	"github.com/aws/amazon-cloudwatch-agent-operator/internal/naming"
	"github.com/aws/amazon-cloudwatch-agent-operator/pkg/constants"
)

// headless label is to differentiate the headless service from the clusterIP service.
const (
	headlessLabel  = "operator.opentelemetry.io/collector-headless-service"
	headlessExists = "Exists"
)

func HeadlessService(params manifests.Params) (*corev1.Service, error) {
	h, err := Service(params)
	if h == nil || err != nil {
		return h, err
	}

	h.Name = naming.HeadlessService(params.OtelCol.Name)
	h.Labels[headlessLabel] = headlessExists

	// copy to avoid modifying params.OtelCol.Annotations
	annotations := map[string]string{
		"service.beta.openshift.io/serving-cert-secret-name": fmt.Sprintf("%s-tls", h.Name),
	}
	for k, v := range h.Annotations {
		annotations[k] = v
	}
	h.Annotations = annotations

	h.Spec.ClusterIP = "None"
	return h, nil
}

func MonitoringService(params manifests.Params) (*corev1.Service, error) {
	name := naming.MonitoringService(params.OtelCol.Name)
	labels := manifestutils.Labels(params.OtelCol.ObjectMeta, name, params.OtelCol.Spec.Image, ComponentAmazonCloudWatchAgent, []string{})

	c, err := adapters.ConfigFromString(params.OtelCol.Spec.Config)
	if err != nil {
		params.Log.Error(err, "couldn't extract the configuration")
		return nil, err
	}

	metricsPort, err := adapters.ConfigToMetricsPort(params.Log, c)
	if err != nil {
		return nil, err
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   params.OtelCol.Namespace,
			Labels:      labels,
			Annotations: params.OtelCol.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector:  manifestutils.SelectorLabels(params.OtelCol.ObjectMeta, ComponentAmazonCloudWatchAgent),
			ClusterIP: "",
			Ports: []corev1.ServicePort{{
				Name: "monitoring",
				Port: metricsPort,
			}},
		},
	}, nil
}

func Service(params manifests.Params) (*corev1.Service, error) {
	name := naming.Service(params.OtelCol.Name)
	labels := manifestutils.Labels(params.OtelCol.ObjectMeta, name, params.OtelCol.Spec.Image, ComponentAmazonCloudWatchAgent, []string{})

	ports := constants.CloudwatchAgentPorts

	if len(params.OtelCol.Spec.Ports) > 0 {
		// we should add all the ports from the CR
		// there are two cases where problems might occur:
		// 1) when the port number is already being used by a receiver
		// 2) same, but for the port name
		//
		// in the first case, we remove the port we inferred from the list
		// in the second case, we rename our inferred port to something like "port-%d"
		portNumbers, portNames := extractPortNumbersAndNames(params.OtelCol.Spec.Ports)
		var resultingInferredPorts []corev1.ServicePort
		for _, inferred := range ports {
			if filtered := filterPort(params.Log, inferred, portNumbers, portNames); filtered != nil {
				resultingInferredPorts = append(resultingInferredPorts, *filtered)
			}
		}

		ports = append(params.OtelCol.Spec.Ports, resultingInferredPorts...)
	}

	// if we have no ports, we don't need a service
	if len(ports) == 0 {

		params.Log.V(1).Info("the instance's configuration didn't yield any ports to open, skipping service", "instance.name", params.OtelCol.Name, "instance.namespace", params.OtelCol.Namespace)
		return nil, nil
	}

	trafficPolicy := corev1.ServiceInternalTrafficPolicyCluster
	if params.OtelCol.Spec.Mode == v1alpha1.ModeDaemonSet {
		trafficPolicy = corev1.ServiceInternalTrafficPolicyLocal
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        naming.Service(params.OtelCol.Name),
			Namespace:   params.OtelCol.Namespace,
			Labels:      labels,
			Annotations: params.OtelCol.Annotations,
		},
		Spec: corev1.ServiceSpec{
			InternalTrafficPolicy: &trafficPolicy,
			Selector:              manifestutils.SelectorLabels(params.OtelCol.ObjectMeta, ComponentAmazonCloudWatchAgent),
			ClusterIP:             "",
			Ports:                 ports,
		},
	}, nil
}

func filterPort(logger logr.Logger, candidate corev1.ServicePort, portNumbers map[int32]bool, portNames map[string]bool) *corev1.ServicePort {
	if portNumbers[candidate.Port] {
		return nil
	}

	// do we have the port name there already?
	if portNames[candidate.Name] {
		// there's already a port with the same name! do we have a 'port-%d' already?
		fallbackName := fmt.Sprintf("port-%d", candidate.Port)
		if portNames[fallbackName] {
			// that wasn't expected, better skip this port
			logger.V(2).Info("a port name specified in the CR clashes with an inferred port name, and the fallback port name clashes with another port name! Skipping this port.",
				"inferred-port-name", candidate.Name,
				"fallback-port-name", fallbackName,
			)
			return nil
		}

		candidate.Name = fallbackName
		return &candidate
	}

	// this port is unique, return as is
	return &candidate
}

func extractPortNumbersAndNames(ports []corev1.ServicePort) (map[int32]bool, map[string]bool) {
	numbers := map[int32]bool{}
	names := map[string]bool{}

	for _, port := range ports {
		numbers[port.Port] = true
		names[port.Name] = true
	}

	return numbers, names
}
