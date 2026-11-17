<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div class="scroll-wrapper" :class="{ 'halloween-theme': eventTheme === 'halloween' }" ref="wrapper">
    <slot />
    <div
      class="custom-scrollbar"
      ref="scrollbar"
      :class="{ ready: isReady, visible: isVisible && canScroll && showScrollbar }"
      @mouseenter="handleMouseEnter"
      @mouseleave="handleMouseLeave"
    >
      <div
        class="thumb no-select"
        ref="thumb"
        :class="{ ready: isReady, visible: isVisible && canScroll && showScrollbar }"
        @mousedown="startDrag"
        @touchstart.prevent="startDrag"
      >
        <div v-if="isNotListing" class="thumb-letters">
          <hr />
        </div>
        <div v-else class="thumb-letters no-select">
          <i class="material-symbols" :class="{ 'primary-icons': thumbIcon === 'push_pin' || isFolder }">
            {{ thumbIcon }}
          </i>
        </div>
      </div>
      <div :class="{ hidden: isNotListing }" class="thumb-section-id no-select" ref="sectionId">
        <span>{{ letter() }}</span>
      </div>
    </div>
  </div>
</template>

<script>
import { state, mutations, getters } from "@/store";
const offsetFromBottomListing = 110;
const offsetFromBottomFull = 75;

export default {
  name: "Scrollbar",
  data() {
    return {
      isDragging: false,
      isHovering: false,
      startY: 0,
      startScrollTop: 0,
      scrollTimeout: null,
      isReady: false,
      isVisible: false,
      scrollFrame: null,
      measureFrame: null,
      canScroll: false,
      resizeObserver: null,
      mutationObserver: null,
    };
  },
  computed: {
    isScrollable() {
      return getters.isScrollable();
    },
    isNotListing() {
      return getters.currentView() !== "listingView";
    },
    isFolder() {
      return this.category() === "folders";
    },
    eventTheme() {
      return getters.eventTheme();
    },
    thumbIcon() {
      const category = this.category();
      if (category === 'pinned') return 'push_pin';
      return this.isFolder ? 'folder' : 'description';
    },
    showScrollbar() {
      const view = getters.currentView();
      return view === 'listingView' || view === 'settings' || view === 'tools' || view === 'markdownViewer';
    },
  },
  methods: {
    handleResize() {
      if (!this.isReady) return;
      this.scheduleMeasure();
    },
    /**
     * One pending measurement per frame. Scroll, resize and DOM mutations all
     * funnel here so a listing mount does not interleave synchronous
     * scrollHeight/clientHeight reads with every mutation callback.
     */
    scheduleMeasure() {
      if (this.measureFrame !== null) return;
      this.measureFrame = requestAnimationFrame(() => {
        this.measureFrame = null;
        this.measureLayout();
      });
    },
    measureLayout() {
      const wrapper = this.$refs.wrapper;
      if (!wrapper) return;
      const scrollHeight = wrapper.scrollHeight;
      const clientHeight = wrapper.clientHeight;
      const scrollableHeight = scrollHeight - clientHeight;
      this._layout = {
        scrollHeight, clientHeight, scrollableHeight,
        thumbHeight: this.$refs.thumb?.clientHeight ?? 0,
        scrollbarHeight: this.$refs.scrollbar?.clientHeight ?? 0,
      };
      const isOverflowing = scrollableHeight > 0;
      if (this.canScroll !== isOverflowing) {
        this.canScroll = isOverflowing;
        if (!isOverflowing) this.isVisible = false;
      }
      if (this.showScrollbar && this.canScroll) {
        this.updateThumbPosition(wrapper.scrollTop);
      }
    },
    category() {
      return state.listing.category;
    },
    letter() {
      return state.listing.letter;
    },
    handleMouseEnter() {
      if (!this.showScrollbar || !this.canScroll) return;
      this.isHovering = true;
      this.isVisible = true;
      this.clearHideTimeout();
    },
    handleMouseLeave() {
      this.isHovering = false;
      this.scheduleHide();
    },
    clearHideTimeout() {
      if (this.scrollTimeout) {
        clearTimeout(this.scrollTimeout);
        this.scrollTimeout = null;
      }
    },
    scheduleHide() {
      this.clearHideTimeout();
      this.scrollTimeout = setTimeout(() => {
        if (!this.isDragging && !this.isHovering) {
          this.isVisible = false;
          mutations.updateListing({ ...state.listing, scrolling: false });
        }
      }, 800);
    },
    handleMouseMove(e) {
      if (!this.showScrollbar || !this.canScroll) return;
      const wrapper = this.$refs.wrapper;
      const bounds = wrapper.getBoundingClientRect();
      const relativeX = e.clientX - bounds.left;
      if (relativeX >= bounds.width - 64) {
        this.isVisible = true;
        this.scheduleHide();
      }
    },
    updateThumbPosition(scrollTop) {
      if (!this.showScrollbar || !this.canScroll) return;
      const thumb = this.$refs.thumb;
      const sectionId = this.$refs.sectionId;
      const cached = this._layout;
      if (!cached || cached.scrollableHeight <= 0 || !thumb) return;
      const scrollRatio = scrollTop / cached.scrollableHeight;
      const maxThumbTop = cached.scrollbarHeight
        - cached.thumbHeight
        - (getters.showStatusBar() ? offsetFromBottomListing : offsetFromBottomFull);
      const thumbPosition = scrollRatio * maxThumbTop;

      // Use transform3d for better performance. Writing the same value would
      // retrigger our own attribute MutationObserver, so only write on change.
      const transform = `translate3d(0, ${thumbPosition}px, 0)`;
      if (thumb.style.transform !== transform) thumb.style.transform = transform;
      if (sectionId.style.transform !== transform) sectionId.style.transform = transform;
    },
    handleScroll() {
      if (!this.isReady) return;
      this.scheduleMeasure();
      if (!this.showScrollbar || !this.canScroll || this.scrollFrame !== null) return;
      // Use requestAnimationFrame to throttle updates
      this.scrollFrame = requestAnimationFrame(() => {
        const content = this.$refs.wrapper;
        if (!content) { this.scrollFrame = null; return; }
        const scrollableHeight = this._layout?.scrollableHeight ?? (content.scrollHeight - content.clientHeight);
        this.isVisible = true;
        this.scheduleHide();
        mutations.setPreviewSource("");
        this.updateThumbPosition(content.scrollTop);
        mutations.updateListing({
          ...state.listing,
          scrolling: true,
          scrollRatio: scrollableHeight > 0
            ? Math.trunc((content.scrollTop / scrollableHeight) * 100)
            : 0,
        });
        this.scrollFrame = null;
      });
    },
    startDrag(e) {
      if (!this.showScrollbar || !this.canScroll) return;
      const clientY = e.touches ? e.touches[0].clientY : e.clientY;
      this.isDragging = true;
      this.startY = clientY;
      this.startScrollTop = this.$refs.wrapper.scrollTop;
      this.clearHideTimeout();

      document.addEventListener("mousemove", this.onDrag);
      document.addEventListener("mouseup", this.stopDrag);
      document.addEventListener("touchmove", this.onDrag, { passive: false });
      document.addEventListener("touchend", this.stopDrag);
    },
    onDrag(e) {
      if (!this.isDragging) return;
      const clientY = e.touches ? e.touches[0].clientY : e.clientY;
      const content = this.$refs.wrapper;
      const scrollbar = this.$refs.scrollbar;
      const thumb = this.$refs.thumb;

      const deltaY = clientY - this.startY;
      const scrollableHeight = this._layout?.scrollableHeight ?? (content.scrollHeight - content.clientHeight);
      const offsetFromBottom = getters.showStatusBar() ? offsetFromBottomListing : offsetFromBottomFull;
      const scrollbarHeight = (this._layout?.scrollbarHeight ?? scrollbar.clientHeight)
        - (this._layout?.thumbHeight ?? thumb.clientHeight)
        - offsetFromBottom;
      const scrollRatio = scrollableHeight / scrollbarHeight;

      let newScrollTop = this.startScrollTop + deltaY * scrollRatio;

      // Clamp scrollTop within bounds
      newScrollTop = Math.max(0, Math.min(newScrollTop, scrollableHeight));
      content.scrollTop = newScrollTop;

      this.updateThumbPosition(newScrollTop);
    },
    stopDrag() {
      this.isDragging = false;
      this.scheduleHide();
      document.removeEventListener("mousemove", this.onDrag);
      document.removeEventListener("mouseup", this.stopDrag);
      document.removeEventListener("touchmove", this.onDrag);
      document.removeEventListener("touchend", this.stopDrag);
    },
  },
  mounted() {
    setTimeout(() => {
      this.isReady = true;
    }, 100);
    this.$refs.wrapper.addEventListener("mousemove", this.handleMouseMove);
    this.$refs.wrapper.addEventListener("scroll", this.handleScroll, { passive: true });
    window.addEventListener("resize", this.handleResize);
    // Observe container size changes
    this.resizeObserver = new ResizeObserver(() => this.scheduleMeasure());
    this.resizeObserver.observe(this.$refs.wrapper);
    // Same here, but with DOM mutations
    this.mutationObserver = new MutationObserver(() => this.scheduleMeasure());
    this.mutationObserver.observe(this.$refs.wrapper, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['style', 'class'],
      characterData: true,
    });
    this.measureLayout();
  },
  beforeUnmount() {
    // Cancel any pending animation frame
    if (this.scrollFrame) {
      cancelAnimationFrame(this.scrollFrame);
    }
    if (this.measureFrame) {
      cancelAnimationFrame(this.measureFrame);
      this.measureFrame = null;
    }
    window.removeEventListener("resize", this.handleResize);
    this.$refs.wrapper.removeEventListener("mousemove", this.handleMouseMove);
    this.$refs.wrapper.removeEventListener("scroll", this.handleScroll);
    if (this.resizeObserver) {
      this.resizeObserver.disconnect();
    }
    if (this.mutationObserver) {
      this.mutationObserver.disconnect();
    }
  },
};
</script>

<style scoped>
.scroll-wrapper {
  position: relative;
  width: 100%;
  height: 100%;
}

.custom-scrollbar {
  position: absolute;
  top: 0;
  right: 0;
  height: 100%;
  display: none;
  width: 2em;
  z-index: 1000;
  pointer-events: none;
  transition: right 0.25s ease;
}

.custom-scrollbar.ready {
  display: block;
}

.thumb.ready {
  display: flex;
}

.thumb.visible {
  right: 0.25em;
}

.thumb {
  right: -5em;
  /* <- Start hidden */
  display: none;
  border: var(--borderWidth) solid var(--background);
  position: fixed;
  top: 4em;
  height: 6em;
  background-color: var(--alt-background);
  border-radius: 1em;
  cursor: pointer;
  pointer-events: auto;
  justify-content: center;
  align-items: center;
  transition: right 0.25s ease, opacity 0.2s;
  z-index: 1001;
}

@supports (backdrop-filter: none) {
  .thumb,
  .thumb-section-id {
    background-color: rgba(237, 237, 237, 0.1) !important;
    backdrop-filter: blur(10px) invert(0.1);
  }
}

.thumb-letters {
  width: 2em;
  height: 2em;
  border-radius: 3em;
  color: var(--textPrimary);
  font-size: 1em;
  display: flex;
  justify-content: center;
  align-items: center;
  pointer-events: none;
  transition: opacity 0.2s;
}

.thumb-section-id {
  top: 5.5em;
  right: 3em;
  width: 3em;
  height: 2.75em;
  background-color: var(--alt-background);
  border-radius: 3em;
  border: var(--borderWidth) solid var(--background);
  font-size: 1em;
  justify-content: center;
  align-items: center;
  pointer-events: none;
  transition: opacity 0.2s;
  position: fixed;
  display: none;
  z-index: 1001;
}

.custom-scrollbar.visible .thumb-section-id {
  display: flex;
}

.thumb, .thumb-section-id {
  will-change: transform;
  transform: translate3d(0, 0, 0);
  backface-visibility: hidden;
  perspective: 1000px;
}
</style>
