/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"context"
	"fmt"
	"net"
	"net/http"

	whhttp "github.com/slok/kubewebhook/pkg/http"
	"k8s.io/apimachinery/pkg/util/sets"
	apiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/httplog"
	"k8s.io/client-go/pkg/version"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/options"
	webhookconsts "github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook/mutating"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook/validating"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

func Run(opt *options.Options, genericOptions ...katalystbase.GenericOptions) error {
	klog.Infof("Version: %+v", version.Get())

	conf, err := opt.Config()
	if err != nil {
		return err
	}

	clientSet, err := client.BuildGenericClient(conf.GenericConfiguration.ClientConnection, opt.MasterURL,
		opt.KubeConfig, fmt.Sprintf("%v", consts.KatalystComponentMetric))
	if err != nil {
		return err
	}

	// set up signals so that we handle the first shutdown signal gracefully.
	ctx := process.SetupSignalHandler()

	webhookCtx, err := katalystbase.NewGenericContext(clientSet, conf.GenericControllerConfiguration.LabelSelector,
		conf.GenericWebhookConfiguration.DynamicGVResources, WebhooksDisabledByDefault, conf.GenericConfiguration,
		consts.KatalystComponentWebhook, nil)
	if err != nil {
		return err
	}

	for _, genericOption := range genericOptions {
		genericOption(webhookCtx)
	}

	// webhook controller ctx first
	webhookCtx.Run(ctx)

	mux := http.NewServeMux()

	defer func() {
		klog.Infoln("webhooks are stopped.")
	}()

	klog.Infoln("ready to start webhooks")
	if err := startWebhooks(ctx, webhookCtx, mux, conf.GenericConfiguration, conf.GenericWebhookConfiguration,
		conf.WebhooksConfiguration, NewWebhookInitializers()); err != nil {
		klog.Fatalf("error starting webhooks: %v", err)
	}

	if err = startInsecureWebhookServer(ctx, conf, mux); err != nil {
		klog.Fatalf("error starting insecure webhook server: %v", err)
	}

	if err = startSecureWebhookServer(ctx, conf, mux); err != nil {
		klog.Fatalf("error starting secure webhook server: %v", err)
	}

	<-ctx.Done()

	klog.Infof("webhook cmd exiting, wait for goroutines exits.")
	return nil
}

// InitFunc is used to launch a particular webhook.
// Any error returned will cause the webhooks process to `Fatal`
// The Webhook indicates the returned webhook implementation, while the bool indicates whether the webhook was enabled.
type InitFunc func(ctx context.Context, webhookCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration,
	webhookGenericConf *webhookconfig.GenericWebhookConfiguration,
	webhookConf *webhookconfig.WebhooksConfiguration,
	webhookName string) (*webhookconsts.WebhookWrapper, error)

// WebhooksDisabledByDefault is the set of webhooks which is disabled by default
var WebhooksDisabledByDefault = sets.NewString()

func NewWebhookInitializers() map[string]InitFunc {
	webhooks := make(map[string]InitFunc)
	webhooks[validating.VPAWebhookName] = validating.StartVPAWebhook
	webhooks[mutating.PodWebhookName] = mutating.StartPodWebhook
	webhooks[mutating.NodeWebhookName] = mutating.StartNodeWebhook
	return webhooks
}

func startWebhooks(ctx context.Context, webhookCtx *katalystbase.GenericContext, mux *http.ServeMux,
	genericConf *generic.GenericConfiguration,
	webhookGenericConf *webhookconfig.GenericWebhookConfiguration,
	webhookConf *webhookconfig.WebhooksConfiguration, webhooks map[string]InitFunc,
) error {
	wrappers := make([]*webhookconsts.WebhookWrapper, 0)
	for webhookName, initFn := range webhooks {
		if !webhookCtx.IsEnabled(webhookName, webhookGenericConf.Webhooks) {
			klog.Warningf("%q is disabled", webhookName)
			continue
		}

		klog.Infof("Starting %q", webhookName)
		webhookWrapper, err := initFn(ctx, webhookCtx, genericConf, webhookGenericConf, webhookConf, webhookName)
		if err != nil {
			klog.Errorf("Error to init %q", webhookName)
			return err
		}
		wrappers = append(wrappers, webhookWrapper)

		klog.Infof("%s initialized", webhookName)
	}

	webhookCtx.StartInformer(ctx)

	for _, wrapper := range wrappers {
		if !wrapper.StartFunc() {
			klog.Errorf("Error to start %q", wrapper.Name)
			return fmt.Errorf("error to start %q", wrapper.Name)
		}
		webhookServer, err := whhttp.HandlerFor(wrapper.Webhook)
		if err != nil {
			klog.Fatalf("create %v webhook handler error: %v", wrapper.Name, err)
		}
		// todo checks whether we should build http chain here for webhook
		mux.Handle("/"+wrapper.Name, httplog.WithLogging(webhookServer, httplog.DefaultStacktracePred))

		klog.Infof("Started %q", wrapper.Name)
	}

	return nil
}

func startInsecureWebhookServer(ctx context.Context, conf *config.Configuration, handler http.Handler) error {
	var (
		listener net.Listener
		err      error
	)

	if len(conf.ServerPort) == 0 {
		klog.Infof("insecure server port is disable")
		return nil
	}

	addr := net.JoinHostPort("0.0.0.0", conf.ServerPort)
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	listener, err = net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	klog.Infof("webhooks listening on %s insecurely...", addr)

	_, _, err = apiserver.RunServer(server, listener, 0, ctx.Done())
	if err != nil {
		return err
	}

	return nil
}

func startSecureWebhookServer(ctx context.Context, conf *config.Configuration, handler http.Handler) error {
	if conf.SecureServing != nil {
		klog.Infof("secure server port is enabled")
		if _, _, err := conf.SecureServing.Serve(handler, 0, ctx.Done()); err != nil {
			return fmt.Errorf("failed to start secure server: %v", err)
		}
	}
	return nil
}
