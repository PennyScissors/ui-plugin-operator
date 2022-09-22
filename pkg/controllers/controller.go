package controllers

import (
	"context"
	"errors"
	"time"

	"github.com/rancher/lasso/pkg/cache"
	"github.com/rancher/lasso/pkg/client"
	"github.com/rancher/lasso/pkg/controller"
	"github.com/rancher/ui-plugin-operator/pkg/controllers/plugin"
	catalog "github.com/rancher/ui-plugin-operator/pkg/generated/controllers/catalog.cattle.io"
	plugincontroller "github.com/rancher/ui-plugin-operator/pkg/generated/controllers/catalog.cattle.io/v1"
	"github.com/rancher/wrangler/pkg/apply"
	"github.com/rancher/wrangler/pkg/generated/controllers/core"
	corecontroller "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	"github.com/rancher/wrangler/pkg/generic"
	"github.com/rancher/wrangler/pkg/leader"
	"github.com/rancher/wrangler/pkg/ratelimit"
	"github.com/rancher/wrangler/pkg/start"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

type AppContext struct {
	plugincontroller.Interface
	K8s      kubernetes.Interface
	Core     corecontroller.Interface
	Apply    apply.Apply
	starters []start.Starter
}

func (a *AppContext) start(ctx context.Context) error {
	return start.All(ctx, 50, a.starters...)
}

func Register(ctx context.Context, appCtx *AppContext, systemNamespace, controllerName, nodeName string, cfg clientcmd.ClientConfig) error {
	if len(systemNamespace) == 0 {
		return errors.New("cannot start controllers on system namespace: system namespace not provided")
	}
	// appCtx, err := NewContext(ctx, systemNamespace, cfg)
	// if err != nil {
	// 	return err
	// }
	if len(controllerName) == 0 {
		controllerName = "plugin-operator"
	}
	plugin.Register(ctx,
		systemNamespace,
		controllerName,
		appCtx.UIPlugin(),
		appCtx.UIPlugin().Cache(),
		appCtx.K8s,
	)
	leader.RunOrDie(ctx, systemNamespace, "plugin-operator-lock", appCtx.K8s, func(ctx context.Context) {
		if err := appCtx.start(ctx); err != nil {
			logrus.Fatal(err)
		}
		logrus.Info("All controllers have been started")
	})

	return nil
}

func controllerFactory(rest *rest.Config) (controller.SharedControllerFactory, error) {
	rateLimit := workqueue.NewItemExponentialFailureRateLimiter(5*time.Millisecond, 60*time.Second)
	clientFactory, err := client.NewSharedClientFactory(rest, nil)
	if err != nil {
		return nil, err
	}

	cacheFactory := cache.NewSharedCachedFactory(clientFactory, nil)
	return controller.NewSharedControllerFactory(cacheFactory, &controller.SharedControllerFactoryOptions{
		DefaultRateLimiter: rateLimit,
		DefaultWorkers:     50,
	}), nil
}

func NewContext(ctx context.Context, systemNamespace string, cfg clientcmd.ClientConfig) (*AppContext, error) {
	client, err := cfg.ClientConfig()
	if err != nil {
		return nil, err
	}
	client.RateLimiter = ratelimit.None

	k8s, err := kubernetes.NewForConfig(client)
	if err != nil {
		return nil, err
	}

	scf, err := controllerFactory(client)
	if err != nil {
		return nil, err
	}

	plugin, err := catalog.NewFactoryFromConfigWithOptions(client, &generic.FactoryOptions{
		Namespace:               systemNamespace,
		SharedControllerFactory: scf,
	})
	if err != nil {
		return nil, err
	}
	pluginv := plugin.Catalog().V1()

	discovery, err := discovery.NewDiscoveryClientForConfig(client)
	if err != nil {
		return nil, err
	}

	core, err := core.NewFactoryFromConfigWithOptions(client, &generic.FactoryOptions{
		SharedControllerFactory: scf,
	})
	if err != nil {
		return nil, err
	}
	corev := core.Core().V1()

	apply := apply.New(discovery, apply.NewClientFactory(client))

	return &AppContext{
		Interface: pluginv,
		K8s:       k8s,
		Core:      corev,
		Apply:     apply,
		starters: []start.Starter{
			plugin,
		},
	}, nil
}
