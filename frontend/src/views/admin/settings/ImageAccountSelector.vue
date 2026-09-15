<template>
  <div class="mt-4 border-t border-gray-100 pt-4 dark:border-dark-700">
    <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ label }}
    </label>
    <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">{{ hint }}</p>
    <div v-if="loading" class="text-sm text-gray-500">加载账号列表...</div>
    <div v-else-if="!accounts.length" class="text-sm text-gray-500">暂无支持生图分组的账号</div>
    <div v-else class="grid gap-2 sm:grid-cols-2">
      <label v-for="account in accounts" :key="account.id" class="flex min-w-0 items-center gap-2 rounded border border-gray-200 px-3 py-2 dark:border-dark-600">
        <input v-model="selected" type="checkbox" :value="account.id" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
        <span class="min-w-0 truncate text-sm text-gray-700 dark:text-gray-200" :title="account.name">
          #{{ account.id }} {{ account.name }}
        </span>
        <span class="ml-auto shrink-0 text-xs text-gray-400">{{ account.platform }}/{{ account.type }}</span>
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue"
import { adminAPI } from "@/api/admin"
import type { ImageGenerationAccountOption } from "@/api/admin/accounts"

const props = defineProps<{ modelValue: number[]; label: string; hint: string }>()
const emit = defineEmits<{ (event: "update:modelValue", value: number[]): void }>()
const accounts = ref<ImageGenerationAccountOption[]>([])
const loading = ref(true)
let imageGenerationAccountsPromise: Promise<ImageGenerationAccountOption[]> | null = null
const selected = computed<number[]>({
  get: () => props.modelValue || [],
  set: value => emit("update:modelValue", [...new Set(value.map(Number).filter(id => id > 0))]),
})

onMounted(async () => {
  try {
    imageGenerationAccountsPromise ||= adminAPI.accounts.listImageGenerationAccounts()
    accounts.value = await imageGenerationAccountsPromise
  } catch {
    accounts.value = []
  }
  finally { loading.value = false }
})
</script>
