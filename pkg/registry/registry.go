package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// GetRemoteDigest queries the container registry for the current digest of the
// image's tag. It returns a string like "sha256:abc123...".
func GetRemoteDigest(ctx context.Context, image string, pullSecrets []corev1.Secret) (string, error) {
	reg, repo, tag := parseImageRef(image)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", reg, repo, tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))

	// Try to set auth from pull secrets.
	setAuth(req, reg, pullSecrets)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HEAD %s: %w", url, err)
	}
	defer resp.Body.Close()

	// Handle 401 with Www-Authenticate: try to get a bearer token.
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := fetchBearerToken(ctx, resp.Header.Get("Www-Authenticate"), reg, pullSecrets)
		if err != nil {
			return "", fmt.Errorf("fetching bearer token: %w", err)
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.docker.distribution.manifest.v2+json",
			"application/vnd.oci.image.manifest.v1+json",
			"application/vnd.oci.image.index.v1+json",
			"application/vnd.docker.distribution.manifest.list.v2+json",
		}, ", "))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("HEAD (authed) %s: %w", url, err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header in response from %s", url)
	}
	return digest, nil
}

// parseImageRef splits a Docker image reference into registry, repository, and tag.
func parseImageRef(image string) (registry, repo, tag string) {
	// Remove tag/digest suffix first.
	tag = "latest"
	ref := image
	if atIdx := strings.Index(ref, "@"); atIdx != -1 {
		ref = ref[:atIdx]
	}
	if colonIdx := strings.LastIndex(ref, ":"); colonIdx != -1 {
		candidate := ref[colonIdx+1:]
		// Make sure this isn't a port number (contains no slash).
		if !strings.Contains(candidate, "/") {
			tag = candidate
			ref = ref[:colonIdx]
		}
	}

	// Split into registry and repository.
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 1 {
		// e.g. "nginx" → Docker Hub library image.
		return "registry-1.docker.io", "library/" + parts[0], tag
	}

	first := parts[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		// Looks like a registry hostname.
		registry = first
		repo = parts[1]
	} else {
		// e.g. "myuser/myimage" → Docker Hub user image.
		registry = "registry-1.docker.io"
		repo = ref
	}

	return registry, repo, tag
}

// dockerConfigJSON represents ~/.docker/config.json structure.
type dockerConfigJSON struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

type dockerAuthEntry struct {
	Auth string `json:"auth"`
}

// setAuth sets Basic auth on the request from imagePullSecrets if available.
func setAuth(req *http.Request, registryHost string, pullSecrets []corev1.Secret) {
	for _, secret := range pullSecrets {
		if secret.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		data, ok := secret.Data[corev1.DockerConfigJsonKey]
		if !ok {
			continue
		}
		var cfg dockerConfigJSON
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		// Try matching the registry host.
		for host, entry := range cfg.Auths {
			if strings.Contains(host, registryHost) && entry.Auth != "" {
				decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
				if err != nil {
					continue
				}
				parts := strings.SplitN(string(decoded), ":", 2)
				if len(parts) == 2 {
					req.SetBasicAuth(parts[0], parts[1])
					return
				}
			}
		}
	}
}

// fetchBearerToken parses a Www-Authenticate header and fetches a bearer token.
func fetchBearerToken(ctx context.Context, wwwAuth string, registryHost string, pullSecrets []corev1.Secret) (string, error) {
	// Parse: Bearer realm="...",service="...",scope="..."
	params := parseWWWAuth(wwwAuth)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("no realm in Www-Authenticate header: %s", wwwAuth)
	}

	tokenURL := realm
	sep := "?"
	if service := params["service"]; service != "" {
		tokenURL += sep + "service=" + service
		sep = "&"
	}
	if scope := params["scope"]; scope != "" {
		tokenURL += sep + "scope=" + scope
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}

	// Add basic auth to token request if we have credentials.
	setAuth(req, registryHost, pullSecrets)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}

	if tokenResp.Token != "" {
		return tokenResp.Token, nil
	}
	if tokenResp.AccessToken != "" {
		return tokenResp.AccessToken, nil
	}
	return "", fmt.Errorf("no token in response from %s", tokenURL)
}

// parseWWWAuth parses a Www-Authenticate header like:
// Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"
func parseWWWAuth(header string) map[string]string {
	result := make(map[string]string)
	header = strings.TrimPrefix(header, "Bearer ")
	header = strings.TrimPrefix(header, "bearer ")

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = strings.Trim(kv[1], "\"")
		}
	}
	return result
}
