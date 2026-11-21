<template>
  <div class="mt-3 space-y-1.5">
    <label :for="fieldId" class="input-label">{{ t('admin.accounts.openai.imageModels') }}</label>
    <textarea
      :id="fieldId"
      :value="modelValue"
      :disabled="disabled"
      class="input min-h-24 font-mono text-sm"
      rows="3"
      data-testid="openai-image-models"
      :placeholder="'seedream-5.5\nvendor-art-v9'"
      @input="$emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
    />
    <p class="input-hint">{{ t('admin.accounts.openai.imageModelsHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(defineProps<{ modelValue: string; disabled?: boolean; fieldId?: string }>(), {
  disabled: false,
  fieldId: 'openai-image-models'
})
defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
</script>
