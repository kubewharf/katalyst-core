package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// InsecureConfig returns a kubelet API config object which uses the token path.
func InsecureConfig(host, tokenFile string) (*rest.Config, error) {
	if tokenFile == "" {
		return nil, fmt.Errorf("api auth token file must be defined")
	}
	if len(host) == 0 {
		return nil, fmt.Errorf("kubelet host must be defined")
	}

	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}

	tlsClientConfig := rest.TLSClientConfig{Insecure: true}

	return &rest.Config{
		Host:            host,
		TLSClientConfig: tlsClientConfig,
		BearerToken:     string(token),
		BearerTokenFile: tokenFile,
	}, nil
}

// GetAndUnmarshalForHttps gets data from the given url and unmarshal it into the given struct.
func GetAndUnmarshalForHttps(ctx context.Context, restConfig *rest.Config, v interface{}) error {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return err
	}

	bytes, err := discoveryClient.RESTClient().
		Get().
		Timeout(httpDefaultTimeout).
		Do(ctx).
		Raw()
	if err != nil {
		return err
	}

	if err = json.Unmarshal(bytes, v); err != nil {
		return fmt.Errorf("failed to unmarshal json for kubelet config: %w", err)
	}

	return nil
}
