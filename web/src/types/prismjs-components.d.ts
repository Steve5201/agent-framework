// prismjs 的组件文件（prism-*.js）是副作用注册脚本（把语法挂到全局 Prism 上），
// 官方类型（@types/prismjs）只覆盖主模块，不覆盖 components/ 子路径。
// 这里按通配声明为副作用模块，供 CodeHighlight 按需注册额外语法（如 Java）。
declare module 'prismjs/components/*' {
  const component: unknown
  export default component
}
