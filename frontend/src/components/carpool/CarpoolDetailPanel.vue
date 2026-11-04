<template>
  <div class="space-y-6">
    <!-- 概览 -->
    <div class="card p-5">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ carpool.name }}</h2>
            <span v-if="carpool.my_role" class="rounded-md bg-gray-100 px-2 py-0.5 text-[11px] text-gray-600 dark:bg-dark-700 dark:text-gray-300">
              {{ t(`carpool.roles.${carpool.my_role}`) }}
            </span>
          </div>
          <p v-if="carpool.notes" class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ carpool.notes }}</p>
        </div>
        <div class="grid grid-cols-2 gap-x-8 gap-y-1 text-sm sm:grid-cols-4">
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('carpool.seats') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">
              {{ t('carpool.seatsUsed', { used: carpool.member_count, total: carpool.seats }) }}
            </div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('carpool.accountWeekly') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">{{ quotaText(carpool.weekly_quota_usd) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('carpool.reserveRatio') }}</div>
            <div class="font-medium text-gray-900 dark:text-white">{{ pct(carpool.reserve_ratio * 100) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('carpool.perSeatWeekly') }}</div>
            <div class="font-semibold text-primary-600 dark:text-primary-400">{{ quotaText(carpool.per_seat_weekly_usd) }}</div>
          </div>
        </div>
      </div>
      <p v-if="!canManage" class="mt-3 text-xs text-amber-600 dark:text-amber-400">{{ t('carpool.readOnlyHint') }}</p>
    </div>

    <div class="grid gap-6 lg:grid-cols-2">
      <!-- 账号 -->
      <div class="card p-5">
        <div class="mb-4 flex items-center justify-between">
          <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('carpool.account.title') }}</h3>
          <span v-if="account" :class="['rounded-full px-2 py-0.5 text-xs', accountHealthy ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300']">
            {{ account.schedulable ? account.status : t('carpool.account.paused') }}
          </span>
        </div>
        <template v-if="account">
          <div class="mb-3 text-sm text-gray-700 dark:text-gray-300">{{ account.name }} <span class="text-xs text-gray-400">#{{ account.id }}</span></div>
          <div v-if="account.error_message" class="mb-3 rounded-lg bg-red-50 p-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
            {{ t('carpool.account.error') }}: {{ account.error_message }}
          </div>
          <div v-if="isFuture(account.rate_limit_reset_at)" class="mb-2 text-xs text-amber-600">
            {{ t('carpool.account.rateLimitedUntil') }} {{ fmtTime(account.rate_limit_reset_at) }}
          </div>
          <div v-if="isFuture(account.temp_unschedulable_until)" class="mb-2 text-xs text-amber-600">
            {{ t('carpool.account.tempUnschedulableUntil') }} {{ fmtTime(account.temp_unschedulable_until) }}
          </div>

          <div v-if="account.five_hour || account.seven_day" class="space-y-3">
            <UsageBar v-if="account.five_hour" :label="t('carpool.account.fiveHour')" :percent="account.five_hour.utilization" :hint="resetHint(account.five_hour.resets_at)" />
            <UsageBar v-if="account.seven_day" :label="t('carpool.account.sevenDay')" :percent="account.seven_day.utilization" :hint="resetHint(account.seven_day.resets_at)" />
          </div>
          <p v-else class="text-xs text-gray-500 dark:text-dark-400">{{ t('carpool.account.noUsage') }}</p>

          <div v-if="canManage" class="mt-4 flex flex-wrap gap-2">
            <button class="btn btn-secondary btn-sm" :disabled="busy" @click="runAction('refresh_usage')">
              <Icon name="refresh" size="sm" class="mr-1" />{{ t('carpool.account.refreshUsage') }}
            </button>
            <button v-if="account.schedulable" class="btn btn-secondary btn-sm" :disabled="busy" @click="runAction('set_schedulable', false)">
              {{ t('carpool.account.pause') }}
            </button>
            <button v-else class="btn btn-primary btn-sm" :disabled="busy" @click="runAction('set_schedulable', true)">
              {{ t('carpool.account.resume') }}
            </button>
            <button v-if="account.error_message || account.status === 'error'" class="btn btn-secondary btn-sm" :disabled="busy" @click="runAction('clear_error')">
              {{ t('carpool.account.clearError') }}
            </button>
            <button v-if="isFuture(account.rate_limit_reset_at)" class="btn btn-secondary btn-sm" :disabled="busy" @click="runAction('clear_rate_limit')">
              {{ t('carpool.account.clearRateLimit') }}
            </button>
          </div>
        </template>
      </div>

      <!-- 周额度推测 -->
      <div class="card p-5">
        <div class="mb-4 flex items-center justify-between">
          <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('carpool.estimate.title') }}</h3>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t(`carpool.quotaSources.${carpool.quota_source}`) }}</span>
        </div>
        <dl class="space-y-3 text-sm">
          <div class="flex items-start justify-between gap-4">
            <dt>
              <div class="text-gray-700 dark:text-gray-300">{{ t('carpool.estimate.estimated') }}</div>
              <div v-if="estimate.sample_used_percent > 0" class="text-xs text-gray-400">
                {{ t('carpool.estimate.sampleFormula', { cost: estimate.sample_cost_usd.toFixed(2), percent: estimate.sample_used_percent.toFixed(1) }) }}
              </div>
              <div v-if="estimate.sample_used_percent < estimate.min_sample_percent" class="text-xs text-gray-400">
                {{ estimate.estimated_weekly_usd > 0
                  ? t('carpool.estimate.newWindowKeep', { min: estimate.min_sample_percent })
                  : t('carpool.estimate.sampleTooSmall', { min: estimate.min_sample_percent }) }}
              </div>
            </dt>
            <dd class="font-medium text-gray-900 dark:text-white">
              {{ estimate.estimated_weekly_usd > 0 ? usd(estimate.estimated_weekly_usd) : '—' }}
            </dd>
          </div>
          <div v-if="estimate.window_reset_at" class="flex items-center justify-between gap-4 text-xs text-gray-500 dark:text-dark-400">
            <span>{{ t('carpool.estimate.windowResetsAt') }}</span>
            <span>{{ fmtTime(estimate.window_reset_at) }}</span>
          </div>
          <div class="flex items-start justify-between gap-4 border-t border-gray-100 pt-3 dark:border-dark-700">
            <dt>
              <div class="font-medium text-gray-900 dark:text-white">{{ t('carpool.estimate.applied') }}</div>
              <div class="text-xs text-gray-400">{{ t(`carpool.quotaSourceHints.${carpool.quota_source}`) }}</div>
            </dt>
            <dd class="text-right">
              <div class="font-semibold text-gray-900 dark:text-white">{{ quotaText(estimate.applied_weekly_usd) }}</div>
              <div class="text-xs text-primary-600 dark:text-primary-400">{{ t('carpool.perSeatWeekly') }} {{ quotaText(estimate.per_seat_weekly_usd) }}</div>
            </dd>
          </div>
        </dl>

        <div v-if="canManage" class="mt-4 flex flex-wrap gap-2">
          <button v-if="carpool.quota_source === 'manual'" class="btn btn-primary btn-sm" :disabled="busy" @click="followEstimate">
            {{ t('carpool.estimate.followAuto') }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleEdit">
            {{ t('carpool.estimate.edit') }}
          </button>
        </div>

        <form v-if="editing" class="mt-4 space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-700/50" @submit.prevent="saveQuota">
          <label class="block text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.reserveRatio') }} (%)
            <input v-model.number="form.reserve_percent" type="number" min="0" max="99" step="1" class="input mt-1" />
            <span class="mt-1 block text-[11px] text-gray-400">{{ t('carpool.reserveRatioHint') }}</span>
          </label>
          <label class="block text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.estimate.manualWeekly') }}
            <input v-model="form.manual_weekly" type="number" min="0" step="0.01" class="input mt-1" />
            <span class="mt-1 block text-[11px] text-gray-400">{{ t('carpool.estimate.manualWeeklyHint') }}</span>
          </label>
          <p v-if="manualWeekly != null" class="text-xs text-primary-600 dark:text-primary-400">
            {{ t('carpool.estimate.preview', { weekly: manualWeekly.toFixed(2), perSeat: previewPerSeat.toFixed(2) }) }}
          </p>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn btn-secondary btn-sm" @click="editing = false">{{ t('common.cancel') }}</button>
            <button type="submit" class="btn btn-primary btn-sm" :disabled="busy">{{ t('carpool.estimate.save') }}</button>
          </div>
        </form>
      </div>
    </div>

    <!-- 成员 -->
    <div class="card overflow-hidden">
      <div class="flex items-center justify-between border-b border-gray-100 p-4 dark:border-dark-700">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('carpool.members.title') }}</h3>
        <slot name="members-toolbar" />
      </div>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-700/50 dark:text-dark-400">
            <tr>
              <th class="px-4 py-2">{{ t('carpool.members.user') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.role') }}</th>
              <th class="min-w-[180px] px-4 py-2">{{ t('carpool.members.weekly') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.daily') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.cost7d') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.requests7d') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.lastUsed') }}</th>
              <th class="px-4 py-2">{{ t('carpool.members.expires') }}</th>
              <th v-if="$slots['member-actions']" class="px-4 py-2"></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="m in detail.members" :key="m.user_id" :class="m.is_me ? 'bg-primary-50/40 dark:bg-primary-900/10' : ''">
              <td class="px-4 py-3">
                <div class="font-medium text-gray-900 dark:text-white">
                  {{ m.username || m.email }}
                  <span v-if="m.is_me" class="ml-1 rounded bg-primary-100 px-1.5 text-[10px] text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">{{ t('carpool.me') }}</span>
                </div>
                <div v-if="m.username" class="text-xs text-gray-400">{{ m.email }}</div>
              </td>
              <td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ t(`carpool.roles.${m.role}`) }}</td>
              <td class="px-4 py-3">
                <UsageBar :percent="m.weekly_limit_usd > 0 ? (m.weekly_usage_usd / m.weekly_limit_usd) * 100 : 0" :hint="`${usd(m.weekly_usage_usd)} / ${quotaText(m.weekly_limit_usd)}`" compact />
                <div v-if="m.weekly_resets_at" class="mt-0.5 text-[11px] text-gray-400">{{ t('carpool.account.resetsAt', { time: fmtTime(m.weekly_resets_at) }) }}</div>
              </td>
              <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ usd(m.daily_usage_usd) }}</td>
              <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ usd(m.cost_7d_usd) }}</td>
              <td class="px-4 py-3 text-gray-700 dark:text-gray-300">{{ m.requests_7d }}</td>
              <td class="px-4 py-3 text-xs text-gray-500">{{ m.last_used_at ? fmtTime(m.last_used_at) : t('carpool.members.never') }}</td>
              <td class="px-4 py-3 text-xs text-gray-500">{{ m.expires_at ? fmtTime(m.expires_at) : '—' }}</td>
              <td v-if="$slots['member-actions']" class="px-4 py-3 text-right">
                <slot name="member-actions" :member="m" />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import Icon from '@/components/icons/Icon.vue'
import UsageBar from './CarpoolUsageBar.vue'
import { formatDateTimeToMinute } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  perSeatWeekly,
  type Carpool,
  type CarpoolAccountAction,
  type CarpoolDetail,
  type CarpoolQuotaRequest
} from '@/api/carpools'

export interface CarpoolPanelActions {
  updateQuota: (id: number, req: CarpoolQuotaRequest) => Promise<Carpool>
  accountAction: (id: number, action: CarpoolAccountAction, schedulable?: boolean) => Promise<void>
}

const props = defineProps<{
  detail: CarpoolDetail
  canManage: boolean
  actions: CarpoolPanelActions
}>()

const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const carpool = computed(() => props.detail.carpool)
const account = computed(() => props.detail.account)
const estimate = computed(() => props.detail.estimate)
const accountHealthy = computed(() => !!account.value && account.value.schedulable && account.value.status === 'active')

const busy = ref(false)
const editing = ref(false)
const form = reactive({
  reserve_percent: 0,
  manual_weekly: '' as string | number
})

// 留空表示跟随自动预估
const manualWeekly = computed(() => {
  const manual = form.manual_weekly === '' ? NaN : Number(form.manual_weekly)
  return Number.isFinite(manual) && manual > 0 ? manual : null
})
const previewPerSeat = computed(() =>
  perSeatWeekly(manualWeekly.value ?? 0, form.reserve_percent / 100, carpool.value.seats)
)

function usd(v: number | undefined | null): string {
  return `$${(v ?? 0).toFixed(2)}`
}

// 额度为 0 表示尚未预估，此时分组不限额
function quotaText(v: number | undefined | null): string {
  return v && v > 0 ? usd(v) : t('carpool.estimate.pending')
}

function pct(v: number): string {
  return `${v.toFixed(0)}%`
}

function fmtTime(v?: string | null): string {
  return v ? formatDateTimeToMinute(v) : ''
}

function isFuture(v?: string | null): boolean {
  return !!v && new Date(v).getTime() > Date.now()
}

function resetHint(v: string | null): string {
  return v ? t('carpool.account.resetsAt', { time: fmtTime(v) }) : ''
}

function toggleEdit() {
  editing.value = !editing.value
  if (editing.value) {
    form.reserve_percent = Math.round(carpool.value.reserve_ratio * 100)
    form.manual_weekly = carpool.value.quota_source === 'manual' ? carpool.value.weekly_quota_usd : ''
  }
}

async function run(fn: () => Promise<unknown>, successMsg: string) {
  busy.value = true
  try {
    await fn()
    appStore.showSuccess(successMsg)
    emit('changed')
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    busy.value = false
  }
}

function runAction(action: CarpoolAccountAction, schedulable?: boolean) {
  return run(() => props.actions.accountAction(carpool.value.id, action, schedulable), t('carpool.account.actionDone'))
}

function followEstimate() {
  return run(() => props.actions.updateQuota(carpool.value.id, { quota_source: 'auto' }), t('carpool.estimate.saved'))
}

function saveQuota() {
  const req: CarpoolQuotaRequest = { reserve_ratio: form.reserve_percent / 100 }
  if (manualWeekly.value != null) {
    req.weekly_quota_usd = manualWeekly.value
  } else {
    req.quota_source = 'auto'
  }
  return run(async () => {
    await props.actions.updateQuota(carpool.value.id, req)
    editing.value = false
  }, t('carpool.estimate.saved'))
}
</script>
