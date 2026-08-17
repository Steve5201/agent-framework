import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// RTL 自动清理：vitest 未开启 globals 时 afterEach 不在全局作用域，
// @testing-library/react 不会自动注册 cleanup，导致用例间 DOM 累积
// （getByRole/getByText 出现"multiple elements"误报）。这里显式注册。
afterEach(() => cleanup())
