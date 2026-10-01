import * as React from 'react';

const COMPLETENESS_LABELS = {
  complete: '完整',
  partial: '部分',
  missing: '缺失',
  legacy_not_applicable: '旧档案不适用',
};

const FOCUS_SELECTOR = [
  'button:not([disabled])',
  '[href]:not([disabled])',
  'summary:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"]):not([disabled])',
].join(',');

function isFocusTarget(target) {
  if (!target || target.hidden || target.disabled) return false;
  if (target.tabIndex === -1) return false;
  if (target.getAttribute?.('aria-hidden') === 'true') return false;
  if (target.getAttribute?.('aria-disabled') === 'true') return false;
  if (target.closest?.('[aria-hidden="true"]') && target.closest?.('[aria-hidden="true"]') !== target) return false;
  const details = target.closest?.('details');
  if (details && !details.open && details.querySelector?.('summary') !== target) return false;
  if (typeof target.getClientRects === 'function' && target.getClientRects().length === 0) return false;
  return true;
}

export function handleEvidenceDrawerKeyDown(event, {
  container,
  activeElement,
  onClose,
} = {}) {
  if (!event) return;
  if (event.key === 'Escape') {
    event.preventDefault?.();
    onClose?.();
    return;
  }
  if (event.key !== 'Tab' || !container?.querySelectorAll) return;
  const targets = [...new Set(container.querySelectorAll(FOCUS_SELECTOR))].filter(isFocusTarget);
  if (!targets.length) return;
  const current = activeElement || globalThis.document?.activeElement;
  const currentIndex = targets.indexOf(current);
  const direction = event.shiftKey ? -1 : 1;
  const nextIndex = currentIndex === -1
    ? 0
    : (currentIndex + direction + targets.length) % targets.length;
  event.preventDefault?.();
  targets[nextIndex].focus?.();
}

export function handleEvidenceDrawerPointerDown(event, onClose) {
  if (!event || event.target !== event.currentTarget) return;
  event.preventDefault?.();
  onClose?.();
}

export function attachEvidenceDrawerDocumentListener(containerRef, onClose) {
  const listener = (event) => handleEvidenceDrawerKeyDown(event, {
    container: containerRef?.current,
    onClose,
  });
  globalThis.document?.addEventListener?.('keydown', listener, { capture: true });
  return () => globalThis.document?.removeEventListener?.('keydown', listener);
}

export function initializeEvidenceDrawerFocus(closeButton) {
  const previousActiveElement = globalThis.document?.activeElement;
  closeButton?.focus?.();
  return () => {
    if (typeof previousActiveElement?.focus === 'function') previousActiveElement.focus();
  };
}

export function useEvidenceDrawerFocus(open, closeButtonRef, dialogRef, onClose) {
  React.useEffect(() => {
    if (!open) return undefined;
    return initializeEvidenceDrawerFocus(closeButtonRef.current);
  }, [closeButtonRef, open]);

  React.useEffect(() => {
    if (!open) return undefined;
    return attachEvidenceDrawerDocumentListener(dialogRef, onClose);
  }, [dialogRef, onClose, open]);
}

export default function EvidenceDrawer({ open = false, groups = [], onClose }) {
  const dialogRef = React.useRef(null);
  const closeRef = React.useRef(null);

  useEvidenceDrawerFocus(open, closeRef, dialogRef, onClose);

  if (!open) return null;

  return (
    <section
      className="opskeeper-evidence-overlay"
      aria-hidden={false}
      onMouseDown={(event) => handleEvidenceDrawerPointerDown(event, onClose)}
      style={{
        '--ops-incident-dialog-surface': 'var(--ops-surface, #ffffff)',
        '--ops-incident-dialog-border': 'var(--ops-surface-border, #94a3b8)',
        '--ops-incident-dialog-foreground': 'var(--ops-surface-foreground, #0f172a)',
        position: 'fixed',
        inset: 0,
        zIndex: 80,
        display: 'grid',
        placeItems: 'center',
        background: 'rgba(15, 23, 42, 0.45)',
        color: 'var(--ops-incident-dialog-foreground)',
        padding: 16,
      }}
    >
      <div
        ref={dialogRef}
        className="opskeeper-evidence-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="opskeeper-evidence-title"
        style={{
          display: 'grid',
          gridTemplateRows: 'auto 1fr',
          width: 'min(880px, 100%)',
          maxHeight: '100%',
          border: '1px solid var(--ops-incident-dialog-border)',
          borderRadius: 8,
          background: 'var(--ops-incident-dialog-surface)',
          padding: 14,
        }}
      >
        <header style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <h2 id="opskeeper-evidence-title" style={{ margin: 0, fontSize: 16 }}>决策证据</h2>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            style={{
              border: '1px solid var(--ops-incident-dialog-border)',
              borderRadius: 4,
              background: 'transparent',
              color: 'inherit',
              font: 'inherit',
              padding: '5px 8px',
            }}
          >
            关闭证据抽屉
          </button>
        </header>
        <div style={{ overflowY: 'auto', padding: '12px 0', display: 'grid', gap: 10, alignContent: 'start' }}>
          {groups.length === 0 && (
            <p style={{ margin: 0, fontSize: 12, color: 'var(--ops-muted-foreground, #475569)' }}>
              暂无可展示的决策证据；请等待只读回读完成。
            </p>
          )}
          {groups.map((group, groupIndex) => (
            <details
              key={group.id || groupIndex}
              open={groupIndex === 0}
              style={{
                border: '1px solid var(--ops-incident-dialog-border)',
                borderRadius: 6,
                padding: 8,
                background: 'var(--ops-incident-dialog-surface)',
              }}
            >
              <summary style={{ cursor: 'pointer', fontWeight: 700 }}>
                {group.title}
                <span style={{ marginLeft: 6, fontWeight: 400 }}>
                  {COMPLETENESS_LABELS[group.completeness] || '状态未知'}
                </span>
              </summary>
              <dl style={{ display: 'grid', gap: 6, margin: '8px 0' }}>
                {(group.facts || []).map((item) => (
                  <div key={item.id} style={{ display: 'grid', gridTemplateColumns: 'minmax(96px, 160px) 1fr', gap: 8 }}>
                    <dt>{item.label}</dt>
                    <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>
                      {item.value === '' || item.value == null ? '缺失' : String(item.value)}
                      {item.sourceIds?.length ? (
                        <small style={{ display: 'block', color: 'var(--ops-muted-foreground, #475569)' }}>
                          来源：{item.sourceIds.join('，')}
                        </small>
                      ) : null}
                    </dd>
                  </div>
                ))}
              </dl>
              <details>
                <summary style={{ cursor: 'pointer', fontSize: 12 }}>原始只读载荷</summary>
                <pre style={{
                  margin: '6px 0 0',
                  padding: 8,
                  border: '1px solid var(--ops-incident-dialog-border)',
                  borderRadius: 4,
                  overflowX: 'auto',
                  fontSize: 11,
                }}>
                  <code>{JSON.stringify(group.rawPayload ?? null, null, 2)}</code>
                </pre>
              </details>
            </details>
          ))}
        </div>
      </div>
    </section>
  );
}
