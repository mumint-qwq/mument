<template>
  <item
    v-for="item in items"
    :key="item.index"
    :index="item.index"
    :style="boxes[item.index]"
    :name="item.name"
    :isDir="item.type === 'directory'"
    :source="source"
    :modified="item.modified"
    :type="item.type"
    :size="item.size"
    :path="item.path"
    :reducedOpacity="item.hidden || isDragging"
    :hash="hash"
    :hasPreview="item.hasPreview"
    :metadata="item.metadata"
    :hasDuration="hasDuration"
    :isShared="item.isShared"
    :pinned="item.pinned"
    :viewToken="item.viewToken"
    @preview-size="$emit('preview-size', item, $event)"
  />
</template>

<script>
import Item from "@/components/files/ListingItem.vue";

/**
 * One immutable chunk of listing cards. Batches keep appending cheap: Vue skips
 * a batch whose props did not change, so a background batch does not re-render
 * the thousands of cards mounted before it.
 */
export default {
  name: "listingItemBatch",
  components: { Item },
  props: {
    items: { type: Array, required: true },
    boxes: { type: Object, required: true },
    source: { type: String, default: "" },
    hash: { type: String, default: "" },
    isDragging: { type: Boolean, default: false },
    hasDuration: { type: Boolean, default: false },
  },
  emits: ["preview-size"],
};
</script>
