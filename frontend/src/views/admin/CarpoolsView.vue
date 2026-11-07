<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-wrap gap-2">
          <button
            v-for="c in carpools"
            :key="c.id"
            :class="['btn btn-sm', c.id === selectedId ? 'btn-primary' : 'btn-secondary']"
            @click="select(c.id)"
          >
            {{ c.name }}
            <span class="ml-1 text-[11px] opacity-70">{{ t('carpool.seatsUsed', { used: c.member_count, total: c.seats }) }}</span>
          </button>
        </div>
        <div class="flex gap-2">
          <button class="btn btn-secondary" :disabled="loading" @click="reload">
            <Icon name="refresh" size="sm" class="mr-1" />{{ t('common.refresh') }}
          </button>
          <button class="btn btn-primary" @click="openCreate">
            <Icon name="plus" size="sm" class="mr-1" />{{ t('carpool.admin.create') }}
          </button>
        </div>
      </div>

      <div v-if="!loading && carpools.length === 0" class="card p-12 text-center text-gray-500 dark:text-dark-400">
        {{ t('carpool.admin.noCarpools') }}
      </div>

      <template v-if="detail">
        <div class="card flex flex-wrap items-end gap-3 p-4">
          <label class="text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.admin.name') }}
            <input v-model="editForm.name" type="text" class="input mt-1 w-48" />
          </label>
          <label class="text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.seats') }}
            <input v-model.number="editForm.seats" type="number" min="1" max="10" class="input mt-1 w-24" />
          </label>
          <label class="text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.validityDays') }}
            <input v-model.number="editForm.validity_days" type="number" min="1" class="input mt-1 w-28" />
          </label>
          <label class="flex-1 text-xs text-gray-600 dark:text-gray-300">
            {{ t('carpool.notes') }}
            <input v-model="editForm.notes" type="text" class="input mt-1 w-full" />
          </label>
          <button class="btn btn-primary btn-sm" :disabled="busy" @click="saveEdit">{{ t('common.save') }}</button>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="renew">{{ t('carpool.admin.renew') }}</button>
          <button class="btn btn-danger btn-sm" :disabled="busy" @click="removeCarpool">{{ t('carpool.admin.delete') }}</button>
          <span class="w-full text-xs text-gray-400">
            {{ t('carpool.admin.groupId', { id: detail.carpool.group_id }) }} · {{ t('carpool.admin.groupHint') }}
            <router-link to="/admin/groups" class="text-primary-600 hover:underline dark:text-primary-400">{{ t('carpool.admin.openGroups') }}</router-link>
          </span>
        </div>

        <CarpoolDetailPanel :detail="detail" :can-manage="true" :actions="carpoolsAdminAPI" @changed="refreshAll">
          <template #members-toolbar>
            <form class="flex gap-2" @submit.prevent="addMember">
              <input v-model="newMemberEmail" type="email" class="input py-1.5 text-sm w-56" :placeholder="t('carpool.members.emailPlaceholder')" />
              <input
                v-model="newMemberShare"
                type="number"
                min="0"
                max="100"
                step="0.01"
                class="input w-28 py-1.5 text-sm"
                :placeholder="t('carpool.members.sharePlaceholder')"
              />
              <select v-model="newMemberRole" class="input py-1.5 text-sm w-28">
                <option value="manager">{{ t('carpool.roles.manager') }}</option>
                <option value="member">{{ t('carpool.roles.member') }}</option>
              </select>
              <button type="submit" class="btn btn-primary btn-sm" :disabled="busy || !newMemberEmail">{{ t('carpool.members.add') }}</button>
            </form>
          </template>
          <template #member-actions="{ member }">
            <div class="flex justify-end gap-2">
              <button class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleRole(member)">
                {{ member.role === 'manager' ? t('carpool.members.setMember') : t('carpool.members.setManager') }}
              </button>
              <button class="btn btn-danger btn-sm" :disabled="busy" @click="removeMember(member)">{{ t('carpool.members.remove') }}</button>
            </div>
          </template>
        </CarpoolDetailPanel>
      </template>
    </div>

    <BaseDialog :show="showCreate" :title="t('carpool.admin.create')" width="wide" @close="showCreate = false">
      <form id="carpool-create-form" class="space-y-4" @submit.prevent="submitCreate">
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('carpool.admin.name') }}
            <input v-model="createForm.name" type="text" class="input mt-1" required />
          </label>
          <label class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('carpool.admin.accountId') }}
            <select v-model.number="createForm.account_id" class="input mt-1" required>
              <option :value="0" disabled>{{ t('carpool.admin.selectAccount') }}</option>
              <option v-for="a in accounts" :key="a.id" :value="a.id">#{{ a.id }} {{ a.name }}</option>
            </select>
          </label>
          <label class="text-sm text-gray-700 dark:text-gray-300 sm:col-span-2">
            {{ t('carpool.admin.adoptGroup') }}
            <select v-model.number="createForm.group_id" class="input mt-1">
              <option :value="0">{{ t('carpool.admin.newGroup') }}</option>
              <option v-for="g in adoptableGroups" :key="g.id" :value="g.id">#{{ g.id }} {{ g.name }}</option>
            </select>
            <span class="mt-1 block text-xs text-gray-400">{{ t('carpool.admin.adoptGroupHint') }}</span>
          </label>
          <label class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('carpool.seats') }}
            <select v-model.number="createForm.seats" class="input mt-1">
              <option v-for="n in [2, 3, 4]" :key="n" :value="n">{{ n }}</option>
            </select>
          </label>
          <label class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('carpool.reserveRatio') }} (%)
            <input v-model.number="createForm.reserve_percent" type="number" min="0" max="99" step="0.1" class="input mt-1" />
          </label>
          <label class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('carpool.validityDays') }}
            <input v-model.number="createForm.validity_days" type="number" min="1" class="input mt-1" />
          </label>
        </div>
        <label class="block text-sm text-gray-700 dark:text-gray-300">
          {{ t('carpool.estimate.manualWeekly') }}
          <input v-model="createForm.manual_weekly" type="number" min="0" step="0.01" class="input mt-1" />
          <span class="mt-1 block text-xs text-gray-400">{{ t('carpool.estimate.manualWeeklyHint') }}</span>
        </label>
        <label v-if="createManualWeekly == null" class="block text-sm text-gray-700 dark:text-gray-300">
          {{ t('carpool.admin.initialQuota') }}
          <input v-model="createForm.initial_weekly" type="number" min="0" step="0.01" class="input mt-1" />
          <span class="mt-1 block text-xs text-gray-400">{{ t('carpool.admin.initialQuotaHint') }}</span>
        </label>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="createForm.count_current_window" type="checkbox" />
          {{ t('carpool.admin.countCurrentWindow') }}
        </label>
        <label class="block text-sm text-gray-700 dark:text-gray-300">
          {{ t('carpool.admin.initialMembers') }}
          <textarea v-model="createForm.member_emails" rows="4" class="input mt-1"></textarea>
        </label>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="createForm.exclusive_account" type="checkbox" />
          {{ t('carpool.admin.exclusiveAccount') }}
        </label>
        <p class="rounded-lg bg-primary-50 p-3 text-sm text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">
          <template v-if="createManualWeekly != null">
            {{ t('carpool.estimate.preview', { weekly: createManualWeekly.toFixed(2), perSeat: createPerSeat.toFixed(2) }) }}
            <br />{{ t('carpool.admin.equalShareHint') }}
          </template>
          <template v-else>{{ t('carpool.admin.autoQuotaHint') }} {{ t('carpool.admin.equalShareHint') }}</template>
        </p>
      </form>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button type="submit" form="carpool-create-form" class="btn btn-primary" :disabled="busy">{{ t('common.confirm') }}</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import CarpoolDetailPanel from '@/components/carpool/CarpoolDetailPanel.vue'
import carpoolsAdminAPI from '@/api/admin/carpools'
import { list as listAccounts } from '@/api/admin/accounts'
import { getAll as listGroups } from '@/api/admin/groups'
import {
  shareWeekly,
  type Carpool,
  type CarpoolDetail,
  type CarpoolMember
} from '@/api/carpools'
import type { AccountListItem, AdminGroup } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const busy = ref(false)
const carpools = ref<Carpool[]>([])
const selectedId = ref<number | null>(null)
const detail = ref<CarpoolDetail | null>(null)
const accounts = ref<AccountListItem[]>([])
const groups = ref<AdminGroup[]>([])
// 可接管的分组：OpenAI 订阅分组，且未被其他拼车使用
const adoptableGroups = computed(() =>
  groups.value.filter(
    (g) => g.subscription_type === 'subscription' && !carpools.value.some((c) => c.group_id === g.id)
  )
)

const editForm = reactive({ name: '', seats: 2, validity_days: 30, notes: '' })
const newMemberEmail = ref('')
const newMemberRole = ref<'manager' | 'member'>('manager')
// 留空则按剩余比例均分给空车位
const newMemberShare = ref<string | number>('')

const showCreate = ref(false)
const createForm = reactive({
  name: '',
  account_id: 0,
  seats: 3,
  reserve_percent: 0,
  validity_days: 30,
  manual_weekly: '' as string | number,
  initial_weekly: '' as string | number,
  group_id: 0,
  count_current_window: false,
  member_emails: '',
  exclusive_account: true
})

// 留空跟随自动预估；填了才是手动额度
const createManualWeekly = computed(() => {
  const manual = createForm.manual_weekly === '' ? NaN : Number(createForm.manual_weekly)
  return Number.isFinite(manual) && manual > 0 ? manual : null
})
// 初始成员按座位均分比例，预览一个座位能分到的额度
const createPerSeat = computed(() =>
  shareWeekly((createManualWeekly.value ?? 0) * (1 - createForm.reserve_percent / 100), 100 / Math.max(createForm.seats, 1))
)

function showErr(err: unknown) {
  appStore.showError(extractApiErrorMessage(err, t('common.error')))
}

async function withBusy(fn: () => Promise<void>, successMsg?: string) {
  busy.value = true
  try {
    await fn()
    if (successMsg) appStore.showSuccess(successMsg)
  } catch (err) {
    showErr(err)
  } finally {
    busy.value = false
  }
}

async function loadList() {
  carpools.value = await carpoolsAdminAPI.list()
}

async function loadDetail() {
  if (selectedId.value == null) {
    detail.value = null
    return
  }
  detail.value = await carpoolsAdminAPI.getDetail(selectedId.value)
  const c = detail.value.carpool
  Object.assign(editForm, { name: c.name, seats: c.seats, validity_days: c.validity_days, notes: c.notes })
}

async function reload() {
  loading.value = true
  try {
    await loadList()
    if (selectedId.value == null || !carpools.value.some((c) => c.id === selectedId.value)) {
      selectedId.value = carpools.value[0]?.id ?? null
    }
    await loadDetail()
  } catch (err) {
    showErr(err)
  } finally {
    loading.value = false
  }
}

async function refreshAll() {
  try {
    await Promise.all([loadList(), loadDetail()])
  } catch (err) {
    showErr(err)
  }
}

function select(id: number) {
  selectedId.value = id
  loadDetail().catch(showErr)
}

async function openCreate() {
  showCreate.value = true
  try {
    const [res, allGroups] = await Promise.all([
      listAccounts(1, 200, { platform: 'openai', lite: '1' }),
      listGroups('openai')
    ])
    accounts.value = res.items
    groups.value = allGroups
  } catch (err) {
    showErr(err)
  }
}

function submitCreate() {
  const emails = createForm.member_emails
    .split(/[\s,;]+/)
    .map((e) => e.trim())
    .filter(Boolean)
  return withBusy(async () => {
    const created = await carpoolsAdminAPI.create({
      name: createForm.name,
      account_id: createForm.account_id,
      weekly_quota_usd: createManualWeekly.value ?? undefined,
      group_id: createForm.group_id || undefined,
      initial_weekly_quota_usd:
        createManualWeekly.value == null && Number(createForm.initial_weekly) > 0 ? Number(createForm.initial_weekly) : undefined,
      count_current_window: createForm.count_current_window,
      seats: createForm.seats,
      reserve_ratio: createForm.reserve_percent / 100,
      validity_days: createForm.validity_days,
      exclusive_account: createForm.exclusive_account,
      members: emails.map((email) => ({ email, role: 'manager' }))
    })
    showCreate.value = false
    selectedId.value = created.id
    await refreshAll()
  }, t('carpool.admin.created'))
}

function saveEdit() {
  if (selectedId.value == null) return
  const id = selectedId.value
  return withBusy(async () => {
    await carpoolsAdminAPI.update(id, { ...editForm })
    await refreshAll()
  }, t('carpool.admin.updated'))
}

function renew() {
  if (selectedId.value == null) return
  const id = selectedId.value
  return withBusy(async () => {
    await carpoolsAdminAPI.renew(id)
    await loadDetail()
  }, t('carpool.admin.renewDone'))
}

function removeCarpool() {
  if (selectedId.value == null || !window.confirm(t('carpool.admin.deleteConfirm'))) return
  const id = selectedId.value
  return withBusy(async () => {
    await carpoolsAdminAPI.remove(id)
    selectedId.value = null
    await reload()
  }, t('carpool.admin.deleted'))
}

function addMember() {
  if (selectedId.value == null) return
  const id = selectedId.value
  return withBusy(async () => {
    const share = newMemberShare.value === '' ? undefined : Number(newMemberShare.value)
    await carpoolsAdminAPI.addMember(id, { email: newMemberEmail.value.trim(), role: newMemberRole.value, share_percent: share })
    newMemberEmail.value = ''
    newMemberShare.value = ''
    await refreshAll()
  }, t('carpool.admin.updated'))
}

function toggleRole(member: CarpoolMember) {
  if (selectedId.value == null) return
  const id = selectedId.value
  return withBusy(async () => {
    await carpoolsAdminAPI.setMemberRole(id, member.user_id, member.role === 'manager' ? 'member' : 'manager')
    await loadDetail()
  }, t('carpool.admin.updated'))
}

function removeMember(member: CarpoolMember) {
  if (selectedId.value == null) return
  if (!window.confirm(t('carpool.members.removeConfirm', { name: member.username || member.email }))) return
  const id = selectedId.value
  return withBusy(async () => {
    await carpoolsAdminAPI.removeMember(id, member.user_id)
    await refreshAll()
  }, t('carpool.admin.updated'))
}

onMounted(reload)
</script>
