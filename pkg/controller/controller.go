package controller

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/example/kube-restarter/pkg/registry"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const annotationKey = "kube-restarter.io/enabled"

// Reconcile finds annotated Deployments, checks image digests, and deletes stale pods.
func Reconcile(ctx context.Context, clientset kubernetes.Interface, namespace string) error {
	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	for _, deploy := range deployments.Items {
		if deploy.Annotations[annotationKey] != "true" {
			continue
		}

		log.Printf("checking deployment %s/%s", deploy.Namespace, deploy.Name)

		// Build label selector from the deployment's selector.
		selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
		if err != nil {
			log.Printf("  error parsing selector: %v", err)
			continue
		}

		pods, err := clientset.CoreV1().Pods(deploy.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			log.Printf("  error listing pods: %v", err)
			continue
		}

		// Collect imagePullSecrets from the pod spec.
		var pullSecrets []corev1.Secret
		if len(pods.Items) > 0 {
			pullSecrets = gatherPullSecrets(ctx, clientset, pods.Items[0])
		}

		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			if shouldDeletePod(ctx, pod, pullSecrets) {
				log.Printf("  deleting stale pod %s/%s", pod.Namespace, pod.Name)
				err := clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
				if err != nil {
					log.Printf("  error deleting pod %s: %v", pod.Name, err)
				}
			}
		}
	}

	return nil
}

// shouldDeletePod checks each container in the pod for a stale image.
func shouldDeletePod(ctx context.Context, pod corev1.Pod, pullSecrets []corev1.Secret) bool {
	for i, container := range pod.Spec.Containers {
		if container.ImagePullPolicy != corev1.PullAlways {
			continue
		}
		if !isLatestTag(container.Image) {
			continue
		}

		remoteDigest, err := registry.GetRemoteDigest(ctx, container.Image, pullSecrets)
		if err != nil {
			log.Printf("  error fetching remote digest for %s: %v", container.Image, err)
			continue
		}

		if i >= len(pod.Status.ContainerStatuses) {
			continue
		}

		runningDigest := extractDigest(pod.Status.ContainerStatuses[i].ImageID)
		if runningDigest == "" {
			log.Printf("  no running digest found for container %s", container.Name)
			continue
		}

		if remoteDigest != runningDigest {
			log.Printf("  container %s is stale: running=%s remote=%s", container.Name, short(runningDigest), short(remoteDigest))
			return true
		}
		log.Printf("  container %s is up to date (%s)", container.Name, short(runningDigest))
	}
	return false
}

// isLatestTag returns true if the image reference uses the "latest" tag or has no tag.
func isLatestTag(image string) bool {
	// If there's an @sha256: digest reference, it's pinned — skip it.
	if strings.Contains(image, "@") {
		return false
	}
	parts := strings.Split(image, ":")
	if len(parts) == 1 {
		return true // no tag means :latest
	}
	return parts[len(parts)-1] == "latest"
}

// extractDigest pulls the sha256:... digest from an imageID like
// "docker-pullable://nginx@sha256:abc123..."
func extractDigest(imageID string) string {
	if idx := strings.Index(imageID, "sha256:"); idx != -1 {
		return imageID[idx:]
	}
	return ""
}

func short(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "..."
	}
	return digest
}

// gatherPullSecrets reads the imagePullSecrets referenced by a pod.
func gatherPullSecrets(ctx context.Context, clientset kubernetes.Interface, pod corev1.Pod) []corev1.Secret {
	var secrets []corev1.Secret
	for _, ref := range pod.Spec.ImagePullSecrets {
		secret, err := clientset.CoreV1().Secrets(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			log.Printf("  warning: could not get pull secret %s: %v", ref.Name, err)
			continue
		}
		secrets = append(secrets, *secret)
	}
	return secrets
}
