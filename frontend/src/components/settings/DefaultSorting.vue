<template>
  <div class="default-sorting form-flex-group">
    <div class="default-sorting-field">
      <ExpandDropdown
        :model-value="currentSorting.by"
        :options="fieldOptions"
        :aria-label="$t('profileSettings.sortField')"
        :disabled="disabled"
        @update:model-value="onFieldChange"
      />
    </div>
    <div class="default-sorting-field">
      <ExpandDropdown
        :model-value="direction"
        :options="directionOptions"
        :aria-label="$t('profileSettings.sortDirection')"
        :disabled="disabled"
        @update:model-value="onDirectionChange"
      />
    </div>
  </div>
</template>

<script>
import ExpandDropdown from "@/components/settings/ExpandDropdown.vue";
import { normalizeSorting } from "@/utils/folderDisplayPreferences.js";

export default {
  name: "DefaultSorting",
  components: {
    ExpandDropdown,
  },
  props: {
    sorting: {
      type: Object,
      default: () => ({ by: "name", asc: true }),
    },
    disabled: {
      type: Boolean,
      default: false,
    },
  },
  emits: ["change"],
  computed: {
    currentSorting() {
      return normalizeSorting(this.sorting) || { by: "name", asc: true };
    },
    direction() {
      return this.currentSorting.asc ? "asc" : "desc";
    },
    fieldOptions() {
      return [
        { value: "name", label: this.$t("general.name") },
        { value: "size", label: this.$t("general.size") },
        { value: "modified", label: this.$t("files.lastModified") },
        { value: "duration", label: this.$t("files.duration") },
        { value: "type", label: this.$t("profileSettings.sortType") },
      ];
    },
    directionOptions() {
      return [
        { value: "asc", label: this.$t("profileSettings.sortAscending") },
        { value: "desc", label: this.$t("profileSettings.sortDescending") },
      ];
    },
  },
  methods: {
    onFieldChange(by) {
      if (this.disabled || by === this.currentSorting.by) {
        return;
      }
      this.$emit("change", { by, asc: this.currentSorting.asc });
    },
    onDirectionChange(direction) {
      if (this.disabled || direction === this.direction) {
        return;
      }
      this.$emit("change", { by: this.currentSorting.by, asc: direction === "asc" });
    },
  },
};
</script>

<style scoped>
.default-sorting {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75em;
  width: 100%;
}

.default-sorting-field {
  flex: 1 1 10em;
  min-width: 8em;
}
</style>
