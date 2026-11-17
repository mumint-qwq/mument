<template>
  <div class="diagnostics-settings">
    <h3 class="diagnostics-title">{{ $t("settings.diagnostics.title") }}</h3>
    <p class="diagnostics-description">{{ $t("settings.diagnostics.description") }}</p>

    <div class="diagnostics-modes" role="group" :aria-label="$t('settings.diagnostics.title')">
      <button
        type="button"
        :class="['button', 'button--flat', ui.mode === 'off' ? 'button--blue' : 'button--grey']"
        :aria-pressed="ui.mode === 'off'"
        @click="setMode('off')"
      >{{ $t("settings.diagnostics.modeOff") }}</button>
      <button
        type="button"
        :class="['button', 'button--flat', ui.mode === 'summary' ? 'button--blue' : 'button--grey']"
        :aria-pressed="ui.mode === 'summary'"
        @click="setMode('summary')"
      >{{ $t("settings.diagnostics.modeSummary") }}</button>
      <button
        type="button"
        :class="['button', 'button--flat', ui.mode === 'detailed' ? 'button--blue' : 'button--grey']"
        :aria-pressed="ui.mode === 'detailed'"
        :disabled="ui.server === 'disabled' || ui.server === 'unsupported'"
        @click="setMode('detailed')"
      >{{ $t("settings.diagnostics.modeDetailed") }}</button>
    </div>

    <dl class="diagnostics-status">
      <dt>{{ $t("settings.diagnostics.status") }}</dt>
      <dd>{{ statusText }}</dd>
      <dt>{{ $t("settings.diagnostics.queue") }}</dt>
      <dd>{{ ui.queue_events }}</dd>
      <dt>{{ $t("settings.diagnostics.historyLabel") }}</dt>
      <dd>{{ $t("settings.diagnostics.history", { count: ui.history_events }) }}</dd>
    </dl>

    <p v-if="!ui.local_only && (ui.server === 'unsupported' || ui.server === 'disabled')" class="diagnostics-hint">
      {{ $t("settings.diagnostics.serverUnavailable") }}
      <button type="button" class="button button--flat button--grey" @click="enableLocalOnly">
        {{ $t("settings.diagnostics.localOnly") }}
      </button>
    </p>

    <div v-if="ui.mode !== 'off'" class="diagnostics-actions">
      <label class="diagnostics-label" for="diagnostics-category">{{ $t("settings.diagnostics.markCategory") }}</label>
      <select id="diagnostics-category" v-model="category">
        <option value="slow_load">{{ $t("settings.diagnostics.catSlow") }}</option>
        <option value="blank_cards">{{ $t("settings.diagnostics.catBlank") }}</option>
        <option value="position_jump">{{ $t("settings.diagnostics.catJump") }}</option>
        <option value="selection_issue">{{ $t("settings.diagnostics.catSelection") }}</option>
        <option value="other">{{ $t("settings.diagnostics.catOther") }}</option>
      </select>
      <button type="button" class="button button--flat button--blue" :disabled="!ui.can_mark" @click="mark">
        {{ $t("settings.diagnostics.mark") }}
      </button>
      <button type="button" class="button button--flat button--grey" @click="exportPage">
        {{ $t("settings.diagnostics.export") }}
      </button>
      <button v-if="ui.transport === 'unsupported' || ui.transport === 'offline' || ui.transport === 'paused'" type="button" class="button button--flat button--grey" @click="retry">
        {{ $t("settings.diagnostics.retry") }}
      </button>
    </div>

    <p v-if="notice" class="diagnostics-notice" role="status">{{ notice }}</p>
    <p class="diagnostics-privacy">{{ $t("settings.diagnostics.privacy") }}</p>
  </div>
</template>

<script>
import { getDiagnosticsRuntime } from "@/utils/diagnostics/runtime.js";

const EMPTY_UI = {
  mode: "off",
  server: "unknown",
  transport: "idle",
  queue_events: 0,
  history_events: 0,
  detailed_remaining_ms: 0,
  can_mark: false,
  can_export: false,
};

export default {
  name: "DiagnosticsSettings",
  data() {
    return {
      ui: { ...EMPTY_UI },
      category: "slow_load",
      notice: "",
      unsubscribe: null,
    };
  },
  computed: {
    statusText() {
      if (this.ui.mode === "off") return this.$t("settings.diagnostics.statusOff");
      if (this.ui.local_only) return this.$t("settings.diagnostics.statusLocal");
      if (this.ui.server === "unsupported") return this.$t("settings.diagnostics.statusUnsupported");
      if (this.ui.server === "disabled") return this.$t("settings.diagnostics.statusDisabled");
      if (this.ui.transport === "paused") return this.$t("settings.diagnostics.statusPaused");
      if (this.ui.transport === "unsupported") return this.$t("settings.diagnostics.statusUnsupported");
      if (this.ui.transport === "offline") return this.$t("settings.diagnostics.statusOffline");
      if (this.ui.transport === "storage_error") return this.$t("settings.diagnostics.statusStorage");
      if (this.ui.transport === "sending" || this.ui.transport === "retry_wait") return this.$t("settings.diagnostics.statusSending");
      if (this.ui.server === "enabled") return this.$t("settings.diagnostics.statusReady");
      return this.$t("settings.diagnostics.statusUnknown");
    },
  },
  mounted() {
    const runtime = getDiagnosticsRuntime();
    if (runtime) {
      this.unsubscribe = runtime.subscribe((state) => {
        this.ui = { ...state };
      });
    }
  },
  beforeUnmount() {
    if (typeof this.unsubscribe === "function") this.unsubscribe();
  },
  methods: {
    runtime() {
      return getDiagnosticsRuntime();
    },
    setMode(mode) {
      const runtime = this.runtime();
      if (!runtime) return;
      runtime.setMode(mode);
      this.notice = "";
    },
    mark() {
      const runtime = this.runtime();
      if (!runtime) return;
      const incident = runtime.markIssue(this.category);
      this.notice = incident
        ? this.$t("settings.diagnostics.marked", { ref: incident })
        : this.$t("settings.diagnostics.markFailed");
    },
    enableLocalOnly() {
      const runtime = this.runtime();
      if (!runtime) return;
      runtime.enableLocalOnly();
      this.notice = this.$t("settings.diagnostics.localOnlyNotice");
    },
    retry() {
      const runtime = this.runtime();
      if (runtime) runtime.retryConnection();
    },
    exportPage() {
      const runtime = this.runtime();
      if (!runtime) return;
      const snapshot = runtime.exportSnapshot();
      const blob = new Blob([JSON.stringify(snapshot, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `quantum-diagnostics-${new Date().toISOString().replace(/[:.]/g, "-")}.json`;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    },
  },
};
</script>

<style scoped>
.diagnostics-settings {
  margin-top: 1.5em;
  padding-top: 1em;
  border-top: 1px solid var(--dividerColor, #ddd);
}
.diagnostics-title {
  margin: 0 0 0.35em;
  font-size: 1em;
}
.diagnostics-description,
.diagnostics-privacy,
.diagnostics-hint {
  color: var(--textSecondary, #666);
  font-size: 0.85em;
}
.diagnostics-modes,
.diagnostics-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5em;
  align-items: center;
  margin: 0.75em 0;
}
.diagnostics-status {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.25em 0.75em;
  margin: 0.5em 0;
  font-size: 0.9em;
}
.diagnostics-status dt {
  font-weight: 600;
}
.diagnostics-status dd {
  margin: 0;
}
.diagnostics-label {
  font-size: 0.9em;
}
.diagnostics-notice {
  color: var(--primaryColor);
  font-size: 0.9em;
}
</style>