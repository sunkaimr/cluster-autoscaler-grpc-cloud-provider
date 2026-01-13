<template>
  <div id="app">
    <!-- Header -->
    <div class="layout-header">
      <el-icon
        :size="24"
        style="cursor: pointer; margin-right: 20px"
        @click="appStore.toggleSidebar"
      >
        <Fold v-if="!appStore.isCollapse" />
        <Expand v-else />
      </el-icon>
      <h1 style="font-size: 20px; font-weight: 500">Cluster Autoscaler Manager</h1>
    </div>

    <!-- Main Container -->
    <div class="layout-container">
      <!-- Sidebar -->
      <el-aside :width="appStore.isCollapse ? '64px' : '200px'" class="layout-sidebar">
        <el-menu
          :default-active="currentRoute"
          :collapse="appStore.isCollapse"
          :collapse-transition="false"
          background-color="#304156"
          text-color="#bfcbd9"
          active-text-color="#409eff"
          router
        >
          <el-menu-item index="/dashboard">
            <el-icon><DataAnalysis /></el-icon>
            <template #title>仪表盘</template>
          </el-menu-item>

          <el-menu-item index="/account">
            <el-icon><User /></el-icon>
            <template #title>云账号管理</template>
          </el-menu-item>

          <el-menu-item index="/instance-parameter">
            <el-icon><Setting /></el-icon>
            <template #title>Instance参数</template>
          </el-menu-item>

          <el-menu-item index="/nodegroup">
            <el-icon><Coin /></el-icon>
            <template #title>NodeGroup</template>
          </el-menu-item>

          <el-menu-item index="/instance">
            <el-icon><Monitor /></el-icon>
            <template #title>Instance</template>
          </el-menu-item>
        </el-menu>
      </el-aside>

      <!-- Main Content -->
      <el-main class="layout-main">
        <router-view />
      </el-main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import {
  Fold,
  Expand,
  DataAnalysis,
  User,
  Setting,
  Coin,
  Monitor
} from '@element-plus/icons-vue'

const route = useRoute()
const appStore = useAppStore()

const currentRoute = computed(() => route.path)
</script>
