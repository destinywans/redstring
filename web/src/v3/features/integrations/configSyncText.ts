import { ApiError } from "../../api/client";
import { useI18n } from "../../i18n";

/**
 * Why a config promotion export (POST /promotions) or production import (POST /config-sync/{id}/import) didn't go
 * through, in words (docs/api/v3-config-sync.md). Known codes get the page's own sentence in the app's language;
 * anything else shows the server's message with its code.
 */
export function useConfigSyncError() {
  const { tx } = useI18n();
  return (e: unknown, op: "export" | "import"): string => {
    if (!(e instanceof ApiError)) return op === "export" ? tx("没能导出，请稍后重试。", "Couldn't export; try again later.") : tx("没能导入，请稍后重试。", "Couldn't import; try again later.");
    const known: Record<string, string> = {
      // Export pre-checks (PA-96, PA-97): refused before anything is reserved or sent to GitLab.
      template_has_description: tx("模板带说明文字；配置提升只携带结构。", "The template has a description; promotion carries structure only."),
      template_retained_content: tx("模板保留了节点标题、步骤或内容；配置提升只携带结构。", "The template keeps node titles, steps or content; promotion carries structure only."),
      template_private_runbooks: tx("模板关联了本实例的 Runbook，生产实例无法对应。", "The template links this instance's Runbooks, which production can't match."),
      template_structure_invalid: tx("模板结构不在配置提升支持的范围内。", "The template structure is outside what promotion supports."),
      alert_rule_expression_unsafe: tx("告警表达式含不能提升的字符串字面量或字符。", "The alert expression has string literals or characters that can't be promoted."),
      alert_rule_label_not_exportable: tx("告警规则带有不能提升的标签或级别（只允许 environment、application、severity）。", "The alert rule has labels or a severity that can't be promoted (only environment, application and severity)."),
      alert_rule_not_exportable: tx("告警规则的名称、分组或持续时间超出配置提升的范围。", "The alert rule's name, group or duration is outside what promotion supports."),
      artifact_ref_not_pinned: tx("Agent 稳定版本的制品引用没有固定到 @sha256 摘要。", "The Agent's stable version artifact isn't pinned to an @sha256 digest."),
      promotion_in_progress: tx("这项配置正在导出，请稍后重试。", "This item is being exported; try again shortly."),
      promotion_conflict: op === "export" ? tx("配置来源或目标已变化，没有导出。", "The source or target changed; nothing was exported.") : tx("配置来源或目标已变化，没有导入。", "The source or target changed; nothing was imported."),
      gitlab_web_url_mismatch: tx("GitLab 返回的合并请求链接不在配置的网页地址下，导出记为结果未知；修正 gitlab.web_url 后重试会复用同一个合并请求。", "GitLab returned a merge request link outside the configured web address, so the export is recorded as unknown; after fixing gitlab.web_url a retry reuses the same merge request."),
      config_source_untrusted: tx("无法验证这条已合并配置的来源（签名、提交或文件不符），没有导入。", "The merged configuration's provenance couldn't be verified (signature, commit or file), so nothing was imported."),
      config_repository_unavailable: tx("配置仓库还没有接入。", "The configuration repository isn't connected yet."),
      promotion_source_unavailable: tx("这类配置的来源还没有接入。", "The source for this kind of configuration isn't connected yet."),
      promotion_kind_unavailable: tx("这类配置还不支持提升。", "This kind of configuration can't be promoted yet."),
      already_production: tx("生产实例不能导出配置。", "Production can't export configuration."),
      production_only: tx("只有生产实例可以导入已合并的配置。", "Only production can import merged configuration."),
      env_view_only: op === "export" ? tx("请先选择一个非生产环境。", "Select one non-production environment first.") : tx("请先选择生产环境。", "Select the production environment first."),
    };
    if (known[e.code]) return known[e.code];
    if (e.status === 403)
      return op === "export" ? tx("你没有导出这项配置的权限。", "You don't have permission to export this item.") : tx("导入需要平台管理员身份和全局 workspace.manage 权限。", "Importing needs a platform administrator with global workspace.manage.");
    if (e.status === 503 && op === "export") return tx("GitLab 现在没有回应，导出结果未知；重试会复用同一个合并请求。", "GitLab didn't answer, so the export result is unknown; a retry reuses the same merge request.");
    return tx("{m}（{c}）", "{m} ({c})", { m: e.message, c: e.code });
  };
}
