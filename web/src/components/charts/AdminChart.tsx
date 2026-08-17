import { useEffect, useRef } from 'react'
import * as echarts from 'echarts/core'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsCoreOption } from 'echarts/core'

// 按需注册管理端统计图表所需类型（与聊天端 EChart 渲染协议组件互不影响）。
// 折线（趋势）/ 柱状（排行备选）/ 饼图（占比）已覆盖数据管理模块全部场景。
echarts.use([
  LineChart,
  BarChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  TitleComponent,
  CanvasRenderer,
])

type EChartsInstance = ReturnType<typeof echarts.init>

/** 管理端统计图表：数据直连构造 option，容器宽度自适应（窗口缩放/侧栏折叠）。 */
export default function AdminChart({ option, height = 280 }: { option: EChartsCoreOption; height?: number }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<EChartsInstance | null>(null)

  // 初始化 + 容器尺寸自适应（页面布局变化时保持图表不溢出）
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const chart = echarts.init(el)
    chartRef.current = chart
    const ro = new ResizeObserver(() => chart.resize())
    ro.observe(el)
    return () => {
      ro.disconnect()
      chart.dispose()
      chartRef.current = null
    }
  }, [])

  // option 变化整体替换，避免窗口切换后残留上一窗口的数据
  useEffect(() => {
    chartRef.current?.setOption(option, true)
  }, [option])

  return <div ref={containerRef} style={{ height, width: '100%' }} />
}
