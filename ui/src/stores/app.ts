import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useAppStore = defineStore('app', () => {
  // 侧边栏是否收起
  const isCollapse = ref(false)

  // 切换侧边栏
  const toggleSidebar = () => {
    isCollapse.value = !isCollapse.value
  }

  return {
    isCollapse,
    toggleSidebar
  }
})
