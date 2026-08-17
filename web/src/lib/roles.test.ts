// roles.ts 单元测试：阶段3·管理员分层的角色判定与资源域解析。
import { describe, expect, it } from 'vitest'
import {
  DEFAULT_AGENT_ID,
  ROLE_LABELS,
  canManageUsers,
  getUserAgentId,
  isAdminRole,
  isSuperAdmin,
} from './roles'

describe('isAdminRole（管理端入口判定）', () => {
  it('三类管理员角色均可进入管理端', () => {
    expect(isAdminRole('super_admin')).toBe(true)
    expect(isAdminRole('agent_admin')).toBe(true)
    expect(isAdminRole('admin')).toBe(true)
  })

  it('普通用户 / 游客 / 未知角色不可进入', () => {
    expect(isAdminRole('user')).toBe(false)
    expect(isAdminRole(undefined)).toBe(false)
    expect(isAdminRole('')).toBe(false)
    expect(isAdminRole('root')).toBe(false)
  })
})

describe('isSuperAdmin（最高超管判定）', () => {
  it('仅 super_admin 为最高超管', () => {
    expect(isSuperAdmin('super_admin')).toBe(true)
    expect(isSuperAdmin('agent_admin')).toBe(false)
    expect(isSuperAdmin('admin')).toBe(false)
    expect(isSuperAdmin('user')).toBe(false)
    expect(isSuperAdmin(undefined)).toBe(false)
  })
})

describe('canManageUsers（用户管理权限）', () => {
  it('super_admin / agent_admin 可管理用户', () => {
    expect(canManageUsers('super_admin')).toBe(true)
    expect(canManageUsers('agent_admin')).toBe(true)
  })

  it('普通管理员与普通用户不可管理用户', () => {
    expect(canManageUsers('admin')).toBe(false)
    expect(canManageUsers('user')).toBe(false)
    expect(canManageUsers(undefined)).toBe(false)
  })
})

describe('getUserAgentId（智能体归属解析）', () => {
  it('存在 agent 标签时返回其值', () => {
    const user = { tags: [{ key: 'agent', value: 'math' }] }
    expect(getUserAgentId(user)).toBe('math')
  })

  it('agent 标签值非字符串首位时返回其值', () => {
    const user = { tags: [{ key: 'agent', value: 'physics-01' }] }
    expect(getUserAgentId(user)).toBe('physics-01')
  })

  it('无标签 / 空标签 / 缺用户时回退默认域', () => {
    expect(getUserAgentId({ tags: [] })).toBe(DEFAULT_AGENT_ID)
    expect(getUserAgentId({ tags: [{ key: 'agent', value: '' }] })).toBe(DEFAULT_AGENT_ID)
    expect(getUserAgentId({})).toBe(DEFAULT_AGENT_ID)
    expect(getUserAgentId(undefined)).toBe(DEFAULT_AGENT_ID)
    expect(getUserAgentId(null)).toBe(DEFAULT_AGENT_ID)
  })

  it('仅识别 key 为 agent 的标签，其它标签忽略', () => {
    const user = { tags: [{ key: 'department', value: 'cs' }] }
    expect(getUserAgentId(user)).toBe(DEFAULT_AGENT_ID)
  })
})

describe('ROLE_LABELS（角色展示名）', () => {
  it('四个角色均有中文展示名', () => {
    expect(ROLE_LABELS.super_admin).toBe('最高超管')
    expect(ROLE_LABELS.agent_admin).toBe('智能体超管')
    expect(ROLE_LABELS.admin).toBe('普通管理员')
    expect(ROLE_LABELS.user).toBe('普通用户')
  })
})
