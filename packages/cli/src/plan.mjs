export function buildSetupPlan(environment) {
  const actions = [];

  if (!environment.larkCli.installed) {
    actions.push({
      id: "install-lark-cli",
      requiresConfirmation: true,
      command: "npx @larksuite/cli@latest install",
      description: "安装飞书官方 lark-cli"
    });
  }

  actions.push({
    id: "configure-lark-cli",
    requiresConfirmation: true,
    command: "lark-cli config init --new",
    description: "配置飞书应用"
  });

  actions.push({
    id: "login-lark-cli",
    requiresConfirmation: true,
    command: "lark-cli auth login --domain im",
    description: "完成飞书 IM 范围的登录授权"
  });

  for (const [agent, installed] of Object.entries(environment.agents)) {
    if (installed) {
      actions.push({
        id: `install-${agent}-plugin`,
        requiresConfirmation: true,
        description: `安装 ${agent} 的 AgentBell Hook 插件`
      });
    }
  }

  return actions;
}
