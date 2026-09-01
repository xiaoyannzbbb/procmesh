export const LOG_PIN_THRESHOLD_PX = 32;

export type ScrollMetrics = {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
};

export function isPinnedToBottom(el: ScrollMetrics): boolean {
  return el.scrollHeight - el.clientHeight - el.scrollTop <= LOG_PIN_THRESHOLD_PX;
}

export function pinToBottom(el: { scrollTop: number; scrollHeight: number }): void {
  el.scrollTop = el.scrollHeight;
}
