/**
 * Remembers which pointer sequence produced a click, so ListingView can tell a
 * scrollbar drag apart from a user clicking blank listing space.
 *
 * The native click that follows a thumb drag can land on the thumb itself or on
 * a common ancestor of the mousedown/mouseup targets (for example #main) when
 * the pointer is released outside the thumb. Checking the click target alone is
 * therefore not enough; the sequence origin is recorded on pointer down and is
 * cleared by the next pointer down, by touch/pointer cancellation, or when the
 * listing unmounts. No timer window is involved.
 */
const SCROLLBAR_SELECTOR = ".custom-scrollbar";

let sequenceStartedInScrollbar = false;

function elementOf(target) {
  if (!target) return null;
  if (typeof target.closest === "function") return target;
  return target.parentElement ?? null;
}

export function isScrollbarRegion(target) {
  const element = elementOf(target);
  return Boolean(element?.closest?.(SCROLLBAR_SELECTOR));
}

/** Record where the pointer sequence that is about to produce a click began. */
export function notePointerSequenceStart(target) {
  sequenceStartedInScrollbar = isScrollbarRegion(target);
}

/**
 * True when this click belongs to a scrollbar interaction: either the click
 * target is inside the scrollbar, or the sequence began there and the click is
 * its first result.
 *
 * `detail` is 0 for keyboard, assistive-technology and programmatic clicks.
 * Those must never be claimed by a stale pointer sequence (for example a right
 * click or a release outside the window that produced no click at all), so the
 * guard still consumes the sequence but reports false for them.
 */
export function consumeScrollbarClick(target, detail = 1) {
  const fromPointer = detail !== 0;
  const fromScrollbar = fromPointer && (sequenceStartedInScrollbar || isScrollbarRegion(target));
  sequenceStartedInScrollbar = false;
  return fromScrollbar;
}

/** Clear the recorded sequence without consuming a click (cancel/unmount). */
export function resetScrollbarClickGuard() {
  sequenceStartedInScrollbar = false;
}

/** Test-only visibility into the recorded origin. */
export function isScrollbarSequenceActive() {
  return sequenceStartedInScrollbar;
}
