<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading && carpools.length === 0" class="flex justify-center py-12">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
      </div>

      <div v-else-if="carpools.length === 0" class="card p-12 text-center">
        <div class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700">
          <Icon name="users" size="xl" class="text-gray-400" />
        </div>
        <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">{{ t('carpool.empty') }}</h3>
        <p class="text-gray-500 dark:text-dark-400">{{ t('carpool.emptyDesc') }}</p>
      </div>

      <template v-else>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div v-if="carpools.length > 1" class="flex flex-wrap gap-2">
            <button
              v-for="c in carpools"
              :key="c.id"
              :class="['btn btn-sm', c.id === selectedId ? 'btn-primary' : 'btn-secondary']"
              @click="select(c.id)"
            >
              {{ c.name }}
            </button>
          </div>
          <span v-else></span>
          <button class="btn btn-secondary btn-sm" :disabled="loading" @click="loadDetail">
            <Icon name="refresh" size="sm" class="mr-1" />{{ t('common.refresh') }}
          </button>
        </div>

        <CarpoolDetailPanel
          v-if="detail"
          :detail="detail"
          :can-manage="detail.carpool.my_role === 'manager'"
          :actions="carpoolsAPI"
          @changed="loadDetail"
        />
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import CarpoolDetailPanel from '@/components/carpool/CarpoolDetailPanel.vue'
import carpoolsAPI, { type Carpool, type CarpoolDetail } from '@/api/carpools'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const carpools = ref<Carpool[]>([])
const selectedId = ref<number | null>(null)
const detail = ref<CarpoolDetail | null>(null)

async function loadDetail() {
  if (selectedId.value == null) return
  loading.value = true
  try {
    detail.value = await carpoolsAPI.getDetail(selectedId.value)
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
}

function select(id: number) {
  selectedId.value = id
  loadDetail()
}

onMounted(async () => {
  loading.value = true
  try {
    carpools.value = await carpoolsAPI.listMine()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
  if (carpools.value.length > 0) select(carpools.value[0].id)
})
</script>
