package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/pkg/pods"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:], os.Getenv))
}

func run(stdout, stderr io.Writer, argv []string, getenv func(string) string) int {
	args, err := pods.ParseCLIArgs(argv)
	if err != nil {
		fmt.Fprintln(stderr, "y-pods:", err)
		fmt.Fprint(stderr, pods.HelpText)
		return 2
	}
	if args.ShowHelp {
		fmt.Fprint(stdout, pods.HelpText)
		return 0
	}
	if args.ShowVersion {
		fmt.Fprintln(stdout, buildinfo.Current().Version)
		return 0
	}

	store := pods.NewStore(getenv("Y_PODS_CONFIG_DIR"))
	mgr := pods.Manager{Store: store, SSH: pods.DefaultSSHClient{}, Getenv: getenv}
	ctx := context.Background()

	switch args.Command {
	case "pods":
		switch args.Subcommand {
		case "list":
			cfg, err := mgr.ListPods()
			if err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			if len(cfg.Pods) == 0 {
				fmt.Fprintln(stdout, "No pods configured. Use 'y-pods pods setup' to add a pod.")
				return 0
			}
			fmt.Fprintln(stdout, "Configured pods:")
			for name, pod := range cfg.Pods {
				marker := " "
				if cfg.Active == name {
					marker = "*"
				}
				gpuInfo := "no GPUs detected"
				if len(pod.GPUs) > 0 {
					gpuInfo = fmt.Sprintf("%dx %s", len(pod.GPUs), pod.GPUs[0].Name)
				}
				vllmInfo := ""
				if pod.VLLMVersion != "" {
					vllmInfo = fmt.Sprintf(" (vLLM: %s)", pod.VLLMVersion)
				}
				fmt.Fprintf(stdout, "%s %s - %s%s - %s\n", marker, name, gpuInfo, vllmInfo, pod.SSH)
				if pod.ModelsPath != "" {
					fmt.Fprintf(stdout, "    Models: %s\n", pod.ModelsPath)
				}
			}
		case "setup":
			if err := mgr.SetupPod(ctx, args.SetupName, args.SetupSSH, args.SetupOpts); err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			fmt.Fprintf(stdout, "Pod '%s' setup complete and set as active.\n", args.SetupName)
		case "active":
			if err := mgr.SetActivePod(args.TargetName); err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			fmt.Fprintf(stdout, "Switched active pod to '%s'.\n", args.TargetName)
		case "remove":
			if err := mgr.RemovePod(args.TargetName); err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			fmt.Fprintf(stdout, "Removed pod '%s' from configuration.\n", args.TargetName)
		}
	case "start":
		if args.Subcommand == "show-models" {
			printKnownModels(stdout, "")
			return 0
		}
		if args.StartName == "" {
			fmt.Fprintln(stderr, "y-pods: --name is required")
			return 1
		}
		if err := mgr.StartModel(ctx, args.StartModelID, args.StartName, args.StartOpts); err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Model '%s' started.\n", args.StartName)
	case "stop":
		if args.TargetName == "" {
			if err := mgr.StopAllModels(ctx, args.PodName); err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			fmt.Fprintln(stdout, "All models stopped.")
		} else {
			if err := mgr.StopModel(ctx, args.TargetName, args.PodName); err != nil {
				fmt.Fprintln(stderr, "y-pods:", err)
				return 1
			}
			fmt.Fprintf(stdout, "Model '%s' stopped.\n", args.TargetName)
		}
	case "list":
		items, podName, err := mgr.ListModels(args.PodName)
		if err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
		if len(items) == 0 {
			fmt.Fprintf(stdout, "No models running on pod '%s'.\n", podName)
			return 0
		}
		fmt.Fprintf(stdout, "Models on pod '%s':\n", podName)
		for _, it := range items {
			gpuStr := fmt.Sprintf("GPU %d", it.GPU[0])
			if len(it.GPU) > 1 {
				gpuStr = fmt.Sprintf("GPUs %v", it.GPU)
			}
			fmt.Fprintf(stdout, "  %s - Port %d - %s - PID %d\n", it.Name, it.Port, gpuStr, it.PID)
			fmt.Fprintf(stdout, "    Model: %s\n", it.Model)
			fmt.Fprintf(stdout, "    URL: http://%s:%d/v1\n", it.Host, it.Port)
		}
	case "shell":
		if err := mgr.Shell(ctx, args.TargetName); err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
	case "ssh":
		res, err := mgr.SSHExec(ctx, args.TargetName, args.SSHCommand)
		if err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
		if res.Stdout != "" {
			fmt.Fprint(stdout, res.Stdout)
		}
		if res.Stderr != "" {
			fmt.Fprint(stderr, res.Stderr)
		}
		return res.ExitCode
	case "logs":
		if err := mgr.Logs(ctx, args.PodName, args.TargetName, pods.LogsOptions{Follow: true}); err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
	case "agent":
		if err := mgr.Agent(ctx, args.PodName, args.TargetName, pods.AgentOptions{
			Messages: args.AgentMessages,
			Continue: args.AgentContinue,
		}); err != nil {
			fmt.Fprintln(stderr, "y-pods:", err)
			return 1
		}
	default:
		fmt.Fprintln(stderr, "y-pods: unknown command:", args.Command)
		fmt.Fprint(stderr, pods.HelpText)
		return 2
	}
	return 0
}

func printKnownModels(w io.Writer, podGPUType string) {
	fmt.Fprintln(w, "Known Models:")
	fmt.Fprintln(w, "Usage: y-pods start <model> --name <name> [options]")
	for id, info := range pods.KnownModels {
		fmt.Fprintf(w, "  %s\n", id)
		fmt.Fprintf(w, "    Name: %s\n", info.Name)
		if info.Notes != "" {
			fmt.Fprintf(w, "    Note: %s\n", info.Notes)
		}
		for _, c := range info.Configs {
			fmt.Fprintf(w, "    Config: %d GPU(s) %v\n", c.GPUCount, c.GPUTypes)
		}
	}
}
