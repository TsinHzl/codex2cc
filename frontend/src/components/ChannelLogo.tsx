import { cn } from "@/lib/utils";
import type { UpstreamChannel } from "@/types";

/**
 * 渠道品牌图标（Codex / Relay 共用 Codex 品牌 mark）。
 *
 * 使用 `@lobehub/icons-static-svg`，对齐 `@lobehub/icons` 的 Codex.Avatar 彩色方案，
 * 但不引入 antd / @lobehub/ui peer deps。
 *
 * - Codex：`codex-color.svg`（白底圆角 + 紫蓝渐变 mark，即 Codex.Avatar）
 */
const ICON_URLS = import.meta.glob(
  ["../../node_modules/@lobehub/icons-static-svg/icons/codex-color.svg"],
  { eager: true, query: "?url", import: "default" },
) as Record<string, string>;

const URL_BY_FILE = (() => {
  const map = new Map<string, string>();
  for (const [path, url] of Object.entries(ICON_URLS)) {
    const file = path.split("/").pop();
    if (file) map.set(file.replace(/\.svg$/, ""), url);
  }
  return map;
})();

export default function ChannelLogo({
  channel,
  size = 14,
  className,
  title,
}: {
  channel: UpstreamChannel;
  size?: number;
  className?: string;
  title?: string;
}) {
  if (channel === "codex" || channel === "relay") {
    const meta = { file: "codex-color", alt: "Codex" };
    const src = URL_BY_FILE.get(meta.file);
    if (!src) return null;
    const rounded = channel === "codex";
    return (
      <img
        src={src}
        alt={title ?? meta.alt}
        title={title}
        width={size}
        height={size}
        draggable={false}
        className={cn(
          "inline-block shrink-0 select-none",
          rounded && "rounded-[20%]",
          className,
        )}
        style={{ width: size, height: size }}
      />
    );
  }

  return null;
}
