import { useEffect, useMemo, useRef } from 'react'
import { Download } from 'lucide-react'
import * as echarts from 'echarts/core'
import { cn } from '@/lib/utils'
import {
  BarChart,
  BoxplotChart,
  CandlestickChart,
  FunnelChart,
  GaugeChart,
  GraphChart,
  HeatmapChart,
  LineChart,
  PieChart,
  RadarChart,
  SankeyChart,
  ScatterChart,
  TreemapChart,
} from 'echarts/charts'
import {
  AriaComponent,
  DataZoomComponent,
  DatasetComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  MarkPointComponent,
  TitleComponent,
  ToolboxComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { parseEChartsOption } from '@/lib/rich'

// 按需注册常用图表（协议白名单）：模型输出的 series.type 属于已注册集合即可渲染。
// 新增图表类型只需在此追加注册，前端渲染逻辑零改动——充分利用模型对 ECharts 标准
// option 的掌握，不用为每种图表写适配代码。
echarts.use([
  BarChart,
  LineChart,
  PieChart,
  RadarChart,
  HeatmapChart,
  ScatterChart,
  FunnelChart,
  GaugeChart,
  TreemapChart,
  GraphChart,
  SankeyChart,
  CandlestickChart,
  BoxplotChart,
  TitleComponent,
  TooltipComponent,
  LegendComponent,
  GridComponent,
  DatasetComponent,
  VisualMapComponent,
  MarkLineComponent,
  MarkPointComponent,
  DataZoomComponent,
  ToolboxComponent,
  AriaComponent,
  CanvasRenderer,
])

type EChartsInstance = ReturnType<typeof echarts.init>

/** ECharts 图表块：解析 ```echarts 代码块里的标准 option 并渲染。
 *  流式中 JSON 未完整时显示"生成中"占位；非法 JSON 显示解析失败占位。
 *
 *  尺寸/对齐协议（见 backend agentsvc/prompt.go）：option 顶层可加 "__media"
 *  { width, height, align } 自定义（渲染前自动剥离，不影响 ECharts）；
 *  也可由语言标签 align 参数传入（```echarts align=center）。 */
export default function EChart({
  source,
  streaming = false,
  height = 320,
  align,
}: {
  source: string
  streaming?: boolean
  height?: number
  align?: string
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<EChartsInstance | null>(null)
  const option = useMemo(() => parseEChartsOption(source), [source])
  // 媒体元数据：__media 为协议扩展键，剥离后传给 ECharts 的 option 须是干净 JSON。
  const media = useMemo<{ width?: number; height?: number; align?: string } | null>(() => {
    if (!option || typeof option.__media !== 'object' || option.__media === null) return null
    return option.__media as { width?: number; height?: number; align?: string }
  }, [option])
  const effAlign = media?.align ?? align
  const chartWidth = typeof media?.width === 'number' && media.width > 0 ? media.width : undefined
  const chartHeight = typeof media?.height === 'number' && media.height > 0 ? media.height : height

  // 初始化 + 容器尺寸自适应（消息宽度变化 / 窗口缩放 / 气泡展开收起）
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

  // option 变化（流式重渲染）时整体替换，避免旧数据残留；先剥离 __media 扩展键。
  useEffect(() => {
    if (!option) return
    const clean = { ...option }
    delete clean.__media // 协议扩展键，剥离后传给 ECharts 的必须是干净 option
    chartRef.current?.setOption(clean, true)
  }, [option])

  // 图表截图下载（ECharts 官方 getDataURL，白底 + 2x 清晰度）
  const handleDownload = () => {
    const chart = chartRef.current
    if (!chart) return
    const dataUrl = chart.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: '#fff' })
    const a = document.createElement('a')
    a.href = dataUrl
    a.download = 'chart.png'
    a.click()
  }

  if (!option) {
    return (
      <div className="my-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        {streaming ? '（图表数据生成中…）' : '（图表数据解析失败：需要标准的 ECharts option JSON）'}
      </div>
    )
  }
  return (
    <div
      className={cn(
        'relative my-2',
        effAlign === 'center' ? 'mx-auto' : effAlign === 'right' ? 'ml-auto' : '',
      )}
      style={chartWidth ? { width: chartWidth } : undefined}
    >
      <button
        type="button"
        title="下载图表图片"
        aria-label="下载图表图片"
        onClick={handleDownload}
        className="absolute right-1.5 top-1.5 z-10 rounded-md border bg-background/90 p-1 text-muted-foreground shadow-sm hover:bg-accent hover:text-accent-foreground"
      >
        <Download className="h-3.5 w-3.5" />
      </button>
      <div ref={containerRef} style={{ height: chartHeight, width: '100%' }} />
    </div>
  )
}
