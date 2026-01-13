<template>
  <el-tag :type="color" size="small">{{ displayName }}</el-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Stage, Status } from '@/types'
import { stageColorMap, statusColorMap, stageNameMap, statusNameMap } from '@/utils'

interface Props {
  value: Stage | Status
  type: 'stage' | 'status'
}

const props = defineProps<Props>()

const color = computed(() => {
  if (props.type === 'stage') {
    return stageColorMap[props.value as Stage]
  }
  return statusColorMap[props.value as Status]
})

const displayName = computed(() => {
  if (props.type === 'stage') {
    return stageNameMap[props.value as Stage] || props.value
  }
  return statusNameMap[props.value as Status] || props.value
})
</script>
