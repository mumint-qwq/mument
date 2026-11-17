<!-- Modified from FileBrowser Quantum for the mument derivative. See MODIFICATIONS.md. -->
<template>
  <div class="card-title">
    <h2>{{ profileSettingsLabel() }}</h2>
  </div>
  <div class="card-content">
    <form>
      <div class="card-content">
        <UserProfilePreferences
          v-model="profileSections"
          :enforced="enforcedPreferences"
          show-extension-inputs
          show-thumbnail-master
          show-device-settings
          show-default-sorting
          default-expanded-section="deviceSettings"
          @change="onPreferenceChange"
          @theme-color="onThemeColor"
          @locale-change="onLocaleChange"
          @reset-device="onResetDevice"
        />
      </div>
    </form>
    <DiagnosticsSettings />
    <br />
  </div>
</template>

<script>
import { notify } from "@/notify";
import { mutations, state, getters } from "@/store";
import UserProfilePreferences from "@/components/settings/UserProfilePreferences.vue";
import DiagnosticsSettings from "@/components/settings/DiagnosticsSettings.vue";
import {
  sectionsFromFlatUser,
  applySectionsToFlatUser,
} from "@/utils/userProfileSections.js";

function cloneUser(user) {
  return JSON.parse(
    JSON.stringify(user ?? { preview: {}, permissions: {} })
  );
}

export default {
  name: "settings",
  components: {
    UserProfilePreferences,
    DiagnosticsSettings,
  },
  data() {
    return {
      localuser: { preview: {}, permissions: {} },
    };
  },
  computed: {
    user() {
      return state.user;
    },
    active() {
      return state.activeSettingsView === "profile-main";
    },
    profileSections: {
      get() {
        return sectionsFromFlatUser(this.localuser);
      },
      set(sections) {
        applySectionsToFlatUser(this.localuser, sections);
      },
    },
    enforcedPreferences() {
      return state.enforcedUserDefaults || {};
    },
  },
  mounted() {
    this.localuser = cloneUser(state.user);
    void mutations.syncEnforcedUserDefaults();
    if (getters.eventTheme() === "halloween" && !state.disableEventThemes) {
      this.localuser.themeColor = "";
    }
    if (typeof this.localuser.showToolsInSidebar !== "boolean") {
      this.localuser.showToolsInSidebar = true;
    }
  },
  methods: {
    profileSettingsLabel() {
      return this.$t("general.profileSettings");
    },
    onThemeColor(color) {
      if (color !== "") {
        document.documentElement.style.setProperty("--primaryColor", color);
      }
    },
    onPreferenceChange(event) {
      // The two account display defaults have their own field-level save path.
      // A generic whole-draft save would replay a stale value after another tab
      // changed the same default, so do not route them through updateSettings.
      if (event?.section === "listing" && event?.field === "viewMode") {
        void this.saveDisplayDefault({ viewMode: this.localuser.viewMode });
        return;
      }
      if (event?.section === "listing" && event?.field === "sorting") {
        void this.saveDisplayDefault({ sorting: this.localuser.sorting });
        return;
      }
      void this.updateSettings();
    },
    async saveDisplayDefault(patch) {
      const ok = await mutations.saveAccountDisplayDefaults(patch);
      // Only update the field this save owns. Replacing the whole draft here
      // would discard unrelated edits (language/theme/toggles) made while the
      // request was in flight.
      if (state.user) {
        if (Object.hasOwn(patch, "viewMode")) {
          this.localuser.viewMode = state.user.viewMode;
        }
        if (Object.hasOwn(patch, "sorting")) {
          this.localuser.sorting = state.user.sorting ? { ...state.user.sorting } : undefined;
        }
      }
      if (ok) {
        notify.showSuccessToast(this.$t("settings.settingsUpdated"));
      }
    },
    onLocaleChange() {
      void this.updateSettings();
    },
    async onResetDevice() {
      try {
        await mutations.resetDeviceSingleClick();
        this.localuser = cloneUser(state.user);
        notify.showSuccessToast(
          this.$t("profileSettings.deviceSettingsReset")
        );
      } catch (e) {
        notify.showError(e?.message || e);
      }
    },
    async updateSettings(event) {
      if (typeof event?.preventDefault === "function") {
        event.preventDefault();
      }
      if (this.localuser.themeColor !== "") {
        document.documentElement.style.setProperty(
          "--primaryColor",
          this.localuser.themeColor
        );
      }
      try {
        const themeChanged = state.user.customTheme !== this.localuser.customTheme;
        // This draft also carries the effective singleClick and the device map
        // from the profile GET. Neither is account-wide user intent: saving an
        // unrelated preference must not rewrite the opening mode or whole
        // device table. The singleClick device control writes through its own
        // mutation, so a stale draft can never push an old value back.
        const draft = cloneUser(this.localuser);
        delete draft.singleClick;
        delete draft.devices;
        // These two are account display defaults saved by saveDisplayDefault.
        // Deleting them prevents a stale draft from reverting a newer value
        // (for example when this page was opened before another tab saved one).
        delete draft.viewMode;
        delete draft.sorting;
        await mutations.updateCurrentUser(draft);
        this.localuser = cloneUser(state.user);
        notify.showSuccessToast(this.$t("settings.settingsUpdated"));
        if (themeChanged) {
          setTimeout(() => {
            window.location.reload();
          }, 1000);
        }
      } catch (e) {
        this.localuser = cloneUser(state.user);
        if (state.user.preview) {
          this.localuser.preview = { ...state.user.preview };
        }
        notify.showError(e?.message || e);
      }
    },
  },
};
</script>

<style scoped>
.card-content :deep(.settings-group) {
  margin-bottom: -0.35em;
}
.settings-group {
  padding-top: 0.5em;
}
</style>