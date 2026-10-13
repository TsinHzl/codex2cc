export type DocsTOCItem = {
  id: string;
  label: string;
  children?: { id: string; label: string; method?: string }[];
};

export default function DocsTOC({
  items,
  title,
  activeId,
  onNavigate,
}: {
  items: DocsTOCItem[];
  title: string;
  activeId: string;
  onNavigate?: () => void;
}) {
  return (
    <nav className="docs-toc" aria-label={title}>
      <div className="docs-toc-title">{title}</div>
      {items.map((item) => (
        <div key={item.id}>
          <a
            href={`#${item.id}`}
            onClick={onNavigate}
            aria-current={activeId === item.id ? "location" : undefined}
            className="docs-toc-parent"
          >
            {item.label}
          </a>
          {item.children?.map((child) => (
            <a
              key={child.id}
              href={`#${child.id}`}
              onClick={onNavigate}
              aria-current={activeId === child.id ? "location" : undefined}
              className="docs-toc-child"
            >
              {child.method && <span>{child.method}</span>}
              <span>{child.label}</span>
            </a>
          ))}
        </div>
      ))}
    </nav>
  );
}
