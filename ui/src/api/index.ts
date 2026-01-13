import axios, { AxiosInstance, AxiosError } from 'axios'
import { ElMessage } from 'element-plus'
import type { ApiResponse } from '@/types'

// 创建 axios 实例
const instance: AxiosInstance = axios.create({
  baseURL: '/api/v1',
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

// 请求拦截器
instance.interceptors.request.use(
  (config) => {
    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// 响应拦截器
instance.interceptors.response.use(
  (response) => {
    const res = response.data as ApiResponse

    // 检查业务状态码
    if (res.status !== 2000000) {
      ElMessage.error(res.error || res.msg || '请求失败')
      return Promise.reject(new Error(res.error || res.msg || '请求失败'))
    }

    return response
  },
  (error: AxiosError) => {
    const message = error.message || '网络请求失败'
    ElMessage.error(message)
    return Promise.reject(error)
  }
)

export default instance
