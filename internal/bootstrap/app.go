package bootstrap

import (
	"context"
	bizagent "github.com/xingexin/catbot/internal/biz/agent"
	bizartifact "github.com/xingexin/catbot/internal/biz/artifact"
	bizconversation "github.com/xingexin/catbot/internal/biz/conversation"
	bizlifecycle "github.com/xingexin/catbot/internal/biz/lifecycle"
	bizmail "github.com/xingexin/catbot/internal/biz/mail"
	bizmessaging "github.com/xingexin/catbot/internal/biz/messaging"
	bizpersona "github.com/xingexin/catbot/internal/biz/persona"
	bizplugin "github.com/xingexin/catbot/internal/biz/plugin"
	bizsystem "github.com/xingexin/catbot/internal/biz/system"
	biztask "github.com/xingexin/catbot/internal/biz/task"
	biztoolcall "github.com/xingexin/catbot/internal/biz/toolcall"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/domain/agent"
	lifecycle "github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/agent/modelapi"
	"github.com/xingexin/catbot/internal/infra/agent/sdkbridge"
	"github.com/xingexin/catbot/internal/infra/auth"
	"github.com/xingexin/catbot/internal/infra/filestore"
	infraplugin "github.com/xingexin/catbot/internal/infra/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
	"github.com/xingexin/catbot/internal/worker/dispatch"
	"github.com/xingexin/catbot/internal/worker/reconcile"
)

// App owns the composed services and their lifecycle, not application use cases.
type App struct {
	Lifecycle    *bizlifecycle.Service
	Store        store.Store
	Options      config.Options
	Vault        *vault.Vault
	Tokens       auth.Tokens
	Conversation *bizconversation.Service
	Personas     *bizpersona.Service
	Agents       *bizagent.Service
	Artifacts    *bizartifact.Service
	Tasks        *biztask.Commands
	Steps        *biztask.StepRunner
	Execution    *biztask.ExecutionHost
	Plugins      *bizplugin.Manager
	Messaging    *bizmessaging.Service
	Tools        *biztoolcall.Service
	Mail         *bizmail.Service
	System       *bizsystem.Service
	Dispatcher   *dispatch.Dispatcher
	reconciler   *reconcile.Worker
}
type taskHost struct {
	steps    *biztask.StepRunner
	messages *bizmessaging.Service
}

func (h taskHost) Step(ctx context.Context, in entity.StepInput) (any, error) {
	return h.steps.Step(ctx, in)
}
func (h taskHost) Notify(ctx context.Context, s entity.Snapshot, text, op string) error {
	return h.messages.Notify(ctx, s, text, op)
}
func New(s store.Store, o config.Options) (*App, error) {
	if o.MaxUploadMB <= 0 {
		o.MaxUploadMB = 100
	}
	v, err := vault.New(s, o.MasterKey)
	if err != nil {
		return nil, err
	}
	a := &App{Store: s, Vault: v, Options: o, Tokens: auth.Tokens{Key: o.MasterKey}}
	packages := infraplugin.NewPackages(s, o.PluginDir, o.DataDir)
	runtime := infraplugin.NewRuntime(o.DataDir, o.InternalURL, a.Tokens.Token)
	a.Plugins = bizplugin.NewWithRuntime(s, v, packages, runtime)
	a.Tasks = &biztask.Commands{Store: s, Plugins: a.Plugins}
	a.Plugins.PauseDependent = a.Tasks.PauseDependent
	a.Tools = &biztoolcall.Service{Store: s, Plugins: a.Plugins, Tasks: a.Tasks}
	a.Conversation = &bizconversation.Service{Store: s, Vault: v, Plugins: a.Plugins, ToolSource: a.Tools}
	a.Conversation.Direct = &agent.Direct{Model: &modelapi.Model{}, Tools: a.Tools}
	a.Conversation.SDK = &sdkbridge.Bridge{URL: o.RuntimeURL, Token: o.RuntimeToken, GatewayURL: o.InternalURL + "/internal/mcp", RunToken: a.Tokens.Token}
	a.Messaging = bizmessaging.New(s, o, a.Conversation)
	a.Conversation.Replies = a.Messaging
	a.Personas = &bizpersona.Service{Store: s}
	a.Agents = &bizagent.Service{Store: s, Vault: v, Options: o}
	a.Artifacts = &bizartifact.Service{Store: s, Files: filestore.Store{Root: o.DataDir}, MaxUploadMB: o.MaxUploadMB}
	a.Mail = &bizmail.Service{Store: s, Tasks: a.Tasks, Plugins: a.Plugins, AuthorizeNotification: a.Messaging.AuthorizeNotification}
	a.Lifecycle = &bizlifecycle.Service{Store: s, Files: a.Artifacts.Files, CheckBindings: a.Messaging.CheckBindingReference, Handlers: map[lifecycle.Resource]bizlifecycle.Handler{lifecycle.ResourceTask: a.Tasks, lifecycle.ResourcePlugin: a.Plugins}}
	a.System = &bizsystem.Service{Store: s, Vault: v, Options: o, CheckBindings: a.Messaging.CheckBindingReference}
	a.Steps = &biztask.StepRunner{Store: s, Plugins: a.Plugins, Agent: a.Conversation}
	a.Execution = &biztask.ExecutionHost{Store: s, Host: taskHost{a.Steps, a.Messaging}}
	a.Dispatcher = dispatch.New(s, a.Conversation)
	a.Conversation.CancelActive = a.Dispatcher.Cancel
	return a, nil
}
func (a *App) Bootstrap(ctx context.Context) error {
	if err := a.Artifacts.Files.Prepare(); err != nil {
		return err
	}
	if err := a.Personas.Bootstrap(ctx); err != nil {
		return err
	}
	if err := a.Conversation.Recover(ctx); err != nil {
		return err
	}
	if err := a.Agents.RecoverModelCalls(ctx); err != nil {
		return err
	}
	return a.registerBundled(ctx)
}
func (a *App) Start() {
	a.reconciler = reconcile.Start(a.Tasks.Reconcile, a.Messaging.ReconcileNotifications)
	a.Dispatcher.Start()
}
func (a *App) Close() { a.reconciler.Close(); a.Dispatcher.Close(); a.Plugins.Close() }
