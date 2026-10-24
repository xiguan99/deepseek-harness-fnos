<template>
  <div v-auto-animate class="w-full flex-1 flex flex-col gap-4 sm:gap-6">
    <!-- 页头标题 -->
    <div
      class="sticky -top-[14px] sm:-top-6 z-20 -mt-3.5 sm:-mt-4 pt-5 sm:pt-7 pb-2 sm:pb-2.5 bg-[#f5f7fa]/90 dark:bg-[#12141a]/90 backdrop-blur-md flex items-center justify-between gap-3 w-full min-w-0 transition-all duration-200">
      <h1 class="text-xl font-bold text-slate-800 dark:text-slate-100 tracking-tight shrink-0">概览</h1>

      <!-- 右侧部署时间 -->
      <span v-if="statusData.build_time" class="text-xs font-mono text-slate-400 dark:text-slate-500 truncate"
        :title="`Deploy: ${statusData.build_time}`">
        Deploy: {{ statusData.build_time }}
      </span>
    </div>

    <template v-if="statusLoaded">
      <!-- 状态监控核心卡片 -->
      <n-card :bordered="false" class="shadow-sm rounded-2xl">
        <div class="flex flex-col py-1 sm:py-2">
          <!-- 上半部分：应用标题/版本 + 右侧主操作（进入 Harness） -->
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
            <div class="space-y-1 min-w-0 flex-1">
              <div class="flex items-center gap-2.5 flex-wrap min-w-0">
                <span
                  class="text-lg sm:text-xl font-bold text-slate-800 dark:text-slate-100 tracking-tight leading-tight truncate">
                  {{ statusData.name || 'DeepSeek Harness' }}
                </span>
                <!-- 更新安装过程中的目标版本动态小徽章 -->
                <n-tag v-if="isBuilding && targetVersion && statusData.version && statusData.version !== targetVersion"
                  type="info" size="small" round :bordered="false"
                  class="font-mono text-xs bg-blue-50 dark:bg-blue-950/50 text-blue-600 dark:text-blue-400 font-semibold px-2 flex items-center gap-1 shadow-sm shrink-0">
                  <template #icon>
                    <n-spin :size="10" class="mr-0.5" />
                  </template>
                  <span>{{ formatVersion(statusData.version) }}</span>
                  <span class="opacity-60">→</span>
                  <span>{{ formatVersion(targetVersion) }}</span>
                </n-tag>
              </div>
              <div
                class="text-xs sm:text-sm text-slate-400 dark:text-slate-500 flex items-center gap-1.5 sm:gap-2 flex-nowrap min-w-0">
                <span class="shrink-0">版本: {{ formatVersion(statusData.version) }}</span>
              </div>
            </div>

            <n-button type="primary" size="medium" :disabled="!isRunning" @click="systemStore.openHarnessApp"
              class="w-full sm:w-auto !h-9 sm:!h-12 px-4 sm:px-7 shadow-sm shadow-fnos-blue/20 font-medium rounded-lg sm:rounded-xl text-sm sm:text-base transition-transform duration-150 active:scale-95">
              <template #icon>
                <n-icon class="text-[17px] sm:text-[20px]">
                  <ExternalLink />
                </n-icon>
              </template>
              <span>进入 Harness</span>
            </n-button>
          </div>

          <!-- 原生分割线 -->
          <n-divider class="!my-3.5 sm:!my-6" />

          <!-- 下半部分：移动端 2 列 (运行时间放底部跨行) / 桌面端 5 列平铺指标网格 -->
          <div class="grid grid-cols-2 sm:grid-cols-5 gap-2 sm:gap-3 text-center">
            <!-- 运行状态 (移动端第 1 行左) -->
            <div
              class="order-1 sm:order-1 py-2 sm:py-3.5 px-3 rounded-xl sm:rounded-2xl bg-slate-50 dark:bg-white/[0.03] border border-slate-100/80 dark:border-white/[0.06] flex flex-col justify-center items-center gap-1 transition-all duration-200 hover:border-slate-200/90 dark:hover:border-white/[0.12] hover:bg-slate-100/50 dark:hover:bg-white/[0.06]">
              <span class="text-xs text-slate-400 dark:text-slate-500 font-medium">运行状态</span>
              <div class="flex items-center justify-center sm:mt-1">
                <n-tag :type="statusTagType" size="small" round :bordered="false"
                  class="font-medium text-xs scale-95 sm:scale-100 transition-all duration-200">
                  <template #icon v-if="isBuilding || isStarting || isSnapshotting">
                    <n-spin :size="10" class="mr-0.5" />
                  </template>
                  {{ statusLabel }}
                </n-tag>
              </div>
            </div>

            <!-- 进程 PID (移动端第 1 行右) -->
            <div
              class="order-2 sm:order-3 py-2 sm:py-3.5 px-3 rounded-xl sm:rounded-2xl bg-slate-50 dark:bg-white/[0.03] border border-slate-100/80 dark:border-white/[0.06] flex flex-col justify-center items-center gap-1 transition-all duration-200 hover:border-slate-200/90 dark:hover:border-white/[0.12] hover:bg-slate-100/50 dark:hover:bg-white/[0.06]">
              <span class="text-xs text-slate-400 dark:text-slate-500 font-medium">进程 PID</span>
              <div
                class="text-xs sm:text-base font-bold text-slate-700 dark:text-slate-200 font-mono truncate sm:mt-0.5"
                :title="isRunning && statusData.pid ? String(statusData.pid) : '-'">
                {{ isRunning && statusData.pid ? statusData.pid : '-' }}
              </div>
            </div>

            <!-- CPU 占用 (移动端第 2 行左) -->
            <div
              class="order-3 sm:order-4 py-2 sm:py-3.5 px-3 rounded-xl sm:rounded-2xl bg-slate-50 dark:bg-white/[0.03] border border-slate-100/80 dark:border-white/[0.06] flex flex-col justify-center items-center gap-1 transition-all duration-200 hover:border-slate-200/90 dark:hover:border-white/[0.12] hover:bg-slate-100/50 dark:hover:bg-white/[0.06]">
              <span class="text-xs text-slate-400 dark:text-slate-500 font-medium">CPU 占用</span>
              <div
                class="text-xs sm:text-base font-bold text-slate-700 dark:text-slate-200 font-mono truncate sm:mt-0.5"
                :title="dshCpu">
                {{ dshCpu }}
              </div>
            </div>

            <!-- 内存占用 (移动端第 2 行右) -->
            <div
              class="order-4 sm:order-5 py-2 sm:py-3.5 px-3 rounded-xl sm:rounded-2xl bg-slate-50 dark:bg-white/[0.03] border border-slate-100/80 dark:border-white/[0.06] flex flex-col justify-center items-center gap-1 transition-all duration-200 hover:border-slate-200/90 dark:hover:border-white/[0.12] hover:bg-slate-100/50 dark:hover:bg-white/[0.06]">
              <span class="text-xs text-slate-400 dark:text-slate-500 font-medium">内存占用</span>
              <div
                class="text-xs sm:text-base font-bold text-slate-700 dark:text-slate-200 font-mono truncate sm:mt-0.5"
                :title="dshMemory">
                {{ dshMemory }}
              </div>
            </div>

            <!-- 运行时间 (移动端底部第 3 行单独全宽跨行 / 桌面端居中第 2 列) -->
            <div
              class="order-5 sm:order-2 col-span-2 sm:col-span-1 py-2 sm:py-3.5 px-3.5 rounded-xl sm:rounded-2xl bg-slate-50 dark:bg-white/[0.03] border border-slate-100/80 dark:border-white/[0.06] flex flex-row sm:flex-col justify-between sm:justify-center items-center gap-1 transition-all duration-200 hover:border-slate-200/90 dark:hover:border-white/[0.12] hover:bg-slate-100/50 dark:hover:bg-white/[0.06]">
              <span class="text-xs text-slate-400 dark:text-slate-500 font-medium">运行时间</span>
              <div
                class="text-xs sm:text-base font-bold text-slate-700 dark:text-slate-200 font-mono truncate sm:mt-0.5">
                {{ uptimeText }}
              </div>
            </div>
          </div>
        </div>
      </n-card>

      <!-- 运行控制区 -->
      <div class="space-y-4">
        <h2 class="text-lg font-bold text-slate-800 dark:text-slate-100 tracking-tight">运行控制</h2>
        <n-grid v-auto-animate :cols="4" :x-gap="10" :y-gap="10" responsive="screen" item-responsive class="sm:!gap-4">
          <n-gi span="2 m:1" v-for="a in actionCards" :key="a.action">
            <n-tooltip trigger="hover" :disabled="isTouch || a.disabled || a.loading">
              <template #trigger>
                <div class="h-full">
                  <!-- 运行控制操作卡片 -->
                  <n-card hoverable :bordered="false"
                    class="cursor-pointer text-center interactive-card select-none !p-1.5 sm:!p-4 shadow-sm group rounded-xl sm:rounded-2xl !h-full"
                    :class="{ 'opacity-50 !cursor-not-allowed !transform-none': a.disabled && !a.loading }"
                    @click="!a.disabled && !a.loading && triggerAction(a)">
                    <div class="flex flex-col items-center justify-center gap-1.5 sm:gap-2.5 py-1.5 sm:py-3">
                      <div
                        class="w-9 h-9 sm:w-12 sm:h-12 rounded-xl sm:rounded-2xl flex items-center justify-center transition-all duration-200 group-hover:scale-110 group-active:scale-95"
                        :class="a.iconBg">
                        <n-spin v-if="a.loading" :size="18" class="sm:hidden" />
                        <n-spin v-if="a.loading" :size="24" class="hidden sm:inline-block" />
                        <n-icon v-else class="text-[18px] sm:text-[24px]" :class="a.iconColor">
                          <component :is="a.icon" />
                        </n-icon>
                      </div>
                      <span
                        class="text-xs sm:text-sm font-medium text-slate-700 dark:text-slate-300 group-hover:text-slate-900 dark:group-hover:text-white transition-colors duration-150">
                        {{ a.label }}
                      </span>
                    </div>
                  </n-card>
                </div>
              </template>
              {{ a.desc }}
            </n-tooltip>
          </n-gi>
        </n-grid>
      </div>
    </template>

    <!-- 底部状态通知区 -->
    <div v-auto-animate class="space-y-3">
      <!-- 实时更新与部署进度 / 启动中 / 快照中 / 错误信息 -->
      <n-alert v-if="statusData.last_message" :type="isBuilding || isStarting || isSnapshotting ? 'info' : 'warning'"
        :show-icon="true" class="rounded-2xl shadow-sm">
        {{ statusData.last_message }}
      </n-alert>

      <!-- 实时连接断开提示 -->
      <n-alert v-if="!wsConnected" type="error" :show-icon="true" class="rounded-2xl shadow-sm">
        实时连接已断开，正在自动重连…
      </n-alert>
    </div>

    <!-- 发现新版本升级确认弹窗（仅在允许在线升级运行时才会出现） -->
    <n-modal v-model:show="showUpdateModal" preset="dialog" type="info" title="发现新版本"
      :positive-text="runtimeUpgradeAllowed ? '立即更新' : undefined"
      negative-text="暂不更新" :loading="isActionLocked" @positive-click="confirmUpgrade"
      @negative-click="showUpdateModal = false">
      <div class="space-y-3 pt-1 text-xs leading-relaxed text-slate-600 dark:text-slate-300">
        <!-- 版本信息对比（上下行信息样式） -->
        <div
          class="p-3 bg-slate-50 dark:bg-white/[0.03] rounded-xl border border-slate-200/70 dark:border-slate-800 space-y-2">
          <div class="flex items-center justify-between text-xs">
            <span class="text-slate-500 dark:text-slate-400">当前版本</span>
            <span class="font-mono font-medium text-slate-700 dark:text-slate-200">
              {{ formatVersion(updateInfo?.current_version || statusData.version) }}
            </span>
          </div>
          <div
            class="flex items-center justify-between text-xs pt-2 border-t border-slate-200/60 dark:border-white/[0.06]">
            <span class="text-blue-600 dark:text-blue-400 font-medium">目标版本</span>
            <span class="font-mono font-semibold text-blue-600 dark:text-blue-400">
              {{ formatVersion(updateInfo?.remote_version || updateInfo?.current_version || statusData.version) }}
            </span>
          </div>
        </div>

        <!-- 警示与快照防护提示区 -->
        <div class="p-3 rounded-xl bg-amber-500/[0.08] dark:bg-amber-500/[0.15] border border-amber-500/30 space-y-2">
          <div class="font-bold text-amber-700 dark:text-amber-400 text-xs tracking-wide flex items-center gap-1.5">
            <n-icon :size="15" class="shrink-0">
              <AlertTriangle />
            </n-icon>
            <span>环境兼容提示</span>
          </div>
          <div class="text-[11.5px] leading-relaxed text-amber-800 dark:text-amber-300">
            新版本可能存在插件或配置变动，建议更新前创建快照以备回滚。
          </div>
          <div
            class="p-2 rounded-lg bg-white/70 dark:bg-black/20 border border-amber-500/20 text-[11px] text-amber-900 dark:text-amber-200 flex items-center justify-between gap-2">
            <span>更新后若遇异常，可随时还原快照或重置。</span>
            <n-button text type="warning" size="tiny" class="font-medium shrink-0" @click="goToSnapshots">
              前往快照 →
            </n-button>
          </div>
        </div>
      </div>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h, type Component } from 'vue'
import { storeToRefs } from 'pinia'
import {
  NCard,
  NDivider,
  NButton,
  NTag,
  NGrid,
  NGi,
  NAlert,
  NSpin,
  NIcon,
  NTooltip,
  NModal,
  useMessage,
  useDialog
} from 'naive-ui'
import {
  ExternalLink,
  PlayerPlay,
  PlayerStop,
  Refresh,
  Download,
  Tools,
  AlertTriangle
} from '@vicons/tabler'
import { useSystemStore } from '../stores/system'
import { useAppStore } from '../stores/app'
import { useConfigStore } from '../stores/config'
import { withAsyncLock } from '../utils/debounce'
import { useIsTouchDevice } from '../utils/device'
import type { CheckUpdateResult } from '../types/api'

const systemStore = useSystemStore()
const appStore = useAppStore()
const configStore = useConfigStore()
const { config: runtimeConfig } = storeToRefs(configStore)
const message = useMessage()
const dialog = useDialog()
const isTouch = useIsTouchDevice()

// 运行时是否允许在线升级：默认 false，即运行时以应用包内钉死的版本为准。
// 关闭时不提供任何"可点击升级"的入口，只展示现状与去路（去应用中心装新版本）。
const runtimeUpgradeAllowed = computed(() => Boolean(runtimeConfig.value.allow_runtime_upgrade))

const showUpdateModal = ref(false)
const updateInfo = ref<CheckUpdateResult | null>(null)
const targetVersion = computed(() => statusData.value.target_version || '')

function goToSnapshots() {
  showUpdateModal.value = false
  appStore.setTab('snapshots')
}

const {
  statusData,
  statusLoaded,
  wsConnected,
  activeAction,
  isCheckingUpdate,
  isActionLocked,
  isRunning,
  isStarting,
  isBuilding,
  isSnapshotting,
  statusTagType,
  statusLabel,
  uptimeText,
  dshCpu,
  dshMemory
} = storeToRefs(systemStore)

onMounted(() => {
  systemStore.fetchInitialStatus()
})

function formatVersion(ver?: string): string {
  if (!ver || ver === '-') return '-'
  const v = ver.replace(/^v/i, '').trim()
  return v ? `v${v}` : '-'
}

interface ActionCard {
  action: string
  icon: Component
  label: string
  desc: string
  iconBg: string
  iconColor: string
  disabled: boolean
  loading: boolean
  confirmText?: string
}

const isRestarting = computed(() => activeAction.value === 'restart')
const isRebuilding = computed(() => activeAction.value === 'rebuild' || (isBuilding.value && Boolean(statusData.value.last_message?.includes('重新部署'))))
const isUpgrading = computed(() => activeAction.value === 'upgrade' || (isBuilding.value && !isRebuilding.value))
const showStopCard = computed(() => isRunning.value || isRestarting.value)

const actionCards = computed<ActionCard[]>(() => [
  showStopCard.value
    ? {
      action: 'stop',
      icon: PlayerStop,
      label: '停止服务',
      desc: '终止 DeepSeek Harness 后台运行进程',
      iconBg: 'bg-rose-50 dark:bg-rose-950/30 group-hover:bg-rose-100 dark:group-hover:bg-rose-950/50',
      iconColor: 'text-rose-600 dark:text-rose-400',
      disabled: isActionLocked.value,
      loading: activeAction.value === 'stop'
    }
    : {
      action: 'start',
      icon: PlayerPlay,
      label: isStarting.value ? '服务启动中' : '启动服务',
      desc: isStarting.value ? '正在拉起服务主进程并等待就绪…' : '拉起 DeepSeek Harness 后台核心服务',
      iconBg: 'bg-emerald-50 dark:bg-emerald-950/30 group-hover:bg-emerald-100 dark:group-hover:bg-emerald-950/50',
      iconColor: 'text-emerald-600 dark:text-emerald-400',
      disabled: isActionLocked.value,
      loading: isStarting.value || activeAction.value === 'start'
    },
  {
    action: 'restart',
    icon: Refresh,
    label: '重启服务',
    desc: '热重启后台进程，即时生效最新配置或插件变更',
    iconBg: 'bg-amber-50 dark:bg-amber-950/30 group-hover:bg-amber-100 dark:group-hover:bg-amber-950/50',
    iconColor: 'text-amber-600 dark:text-amber-400',
    disabled: !isRestarting.value && (!isRunning.value || isActionLocked.value),
    loading: isRestarting.value
  },
  {
    action: 'check_update',
    icon: Download,
    label: isCheckingUpdate.value ? '检查中…' : '检查更新',
    // 关闭在线升级时，这张卡不再是"去升级"的入口，只说明运行时随包发布、升级走应用中心
    desc: runtimeUpgradeAllowed.value
      ? '检测 NPM 上游最新版本，确认后可在线升级运行时'
      : `运行时随包发布${statusData.value.version ? `（${formatVersion(statusData.value.version)}）` : ''}，升级请到应用中心安装新版本`,
    iconBg: 'bg-blue-50 dark:bg-blue-950/30 group-hover:bg-blue-100 dark:group-hover:bg-blue-950/50',
    iconColor: 'text-fnos-blue dark:text-blue-400',
    disabled: isActionLocked.value || isCheckingUpdate.value,
    loading: isCheckingUpdate.value || isUpgrading.value
  },
  {
    action: 'rebuild',
    icon: Tools,
    label: '重新部署',
    desc: '清空本地依赖缓存，重新在线安装当前版本 DSH',
    iconBg: 'bg-purple-50 dark:bg-purple-950/30 group-hover:bg-purple-100 dark:group-hover:bg-purple-950/50',
    iconColor: 'text-purple-600 dark:text-purple-400',
    disabled: isActionLocked.value && !isRebuilding.value,
    loading: isRebuilding.value,
    confirmText: '此操作将清理本地依赖缓存并重新安装当前版本的 DSH，确定继续？'
  }
])

const confirmUpgrade = async () => {
  showUpdateModal.value = false
  const res = await systemStore.sendAction('upgrade')
  if (res.success) {
    message.success(res.message || '已开始拉取更新并部署')
  } else {
    message.error(res.message || '更新启动失败')
  }
}

const handleAction = withAsyncLock(async (action: string) => {
  if (action === 'check_update') {
    const res = await systemStore.checkUpdate()
    if (!res.success) {
      message.error(res.message || '检查更新失败，请检查网络')
      return
    }

    // 运行时不跟随 NPM 在线升级（默认）：只汇报事实，不弹"立即更新"，不给可点入口。
    // 否则用户会看到一个点下去必然失败的更新按钮，误以为应用坏了。
    if (!runtimeUpgradeAllowed.value) {
      const current = formatVersion(res.data.current_version || statusData.value.version || '')
      const remote = res.data.has_update ? formatVersion(res.data.remote_version || '') : ''
      dialog.info({
        title: '运行时版本',
        content: () =>
          h('div', { class: 'space-y-2 text-xs leading-relaxed' }, [
            h('div', `运行时已钉死 ${current}（随包发布）`),
            h(
              'div',
              { class: 'text-slate-500' },
              '在线升级已关闭，可在「设置 → 网络与依赖源」开启'
            ),
            h(
              'div',
              { class: 'text-slate-500' },
              remote
                ? `上游已发布 ${remote}；本应用跟随应用中心的应用包更新，不需要单独升级运行时`
                : '当前运行时与包内版本一致'
            )
          ]),
        positiveText: '知道了'
      })
      return
    }

    if (res.data.has_update) {
      updateInfo.value = res.data
      showUpdateModal.value = true
    } else {
      message.info(res.data.message || '当前已是最新版本')
    }
    return
  }

  const res = await systemStore.sendAction(action)
  if (res.success) {
    message.success(res.message || '操作成功')
  } else {
    message.error(res.message || '操作失败')
  }
})

// 统一动作触发
function triggerAction(a: ActionCard) {
  if (a.confirmText) {
    dialog.warning({
      title: `确认${a.label}？`,
      content: a.confirmText,
      positiveText: '确认',
      negativeText: '取消',
      onPositiveClick: () => {
        void handleAction(a.action)
      }
    })
    return
  }
  void handleAction(a.action)
}
</script>