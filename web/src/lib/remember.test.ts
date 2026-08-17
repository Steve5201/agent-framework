// remember.ts 单元测试：按智能体域隔离的 localStorage 实现。
// 覆盖：读写回环、空输入不落盘、clear 幂等、损坏数据兜底、域隔离、旧 key 兼容迁移。
import { beforeEach, describe, expect, it } from 'vitest'
import { clearRemembered, loadRemembered, saveRemembered } from './remember'

describe('remember（按域隔离 localStorage）', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('保存后能读回同一份凭据', async () => {
    await saveRemembered('tutor', 'alice', 'Passw0rd1')
    expect(await loadRemembered('tutor')).toEqual({ username: 'alice', password: 'Passw0rd1' })
  })

  it('空用户名或空密码不落盘', async () => {
    await saveRemembered('tutor', '', 'Passw0rd1')
    await saveRemembered('tutor', 'alice', '')
    expect(await loadRemembered('tutor')).toBeNull()
    expect(localStorage.getItem('agent.remembered_credentials.tutor')).toBeNull()
  })

  it('clear 后读取为 null（幂等）', async () => {
    await saveRemembered('tutor', 'alice', 'Passw0rd1')
    await clearRemembered('tutor')
    expect(await loadRemembered('tutor')).toBeNull()
    await clearRemembered('tutor') // 已清除再清 → 不报错
  })

  it('损坏数据返回 null 不抛错', async () => {
    localStorage.setItem('agent.remembered_credentials.tutor', 'not-a-base64-json!!!')
    expect(await loadRemembered('tutor')).toBeNull()
  })

  it('base64 合法但字段缺失的数据返回 null', async () => {
    localStorage.setItem(
      'agent.remembered_credentials.tutor',
      btoa(JSON.stringify({ username: 'x' })),
    )
    expect(await loadRemembered('tutor')).toBeNull()
  })

  it('不同门户域各自保存互不影响', async () => {
    await saveRemembered('tutor', 'alice', 'Passw0rd1')
    await saveRemembered('math', 'bob', 'Passw0rd2')
    await saveRemembered('*', 'root', 'Passw0rd3')
    expect(await loadRemembered('tutor')).toEqual({ username: 'alice', password: 'Passw0rd1' })
    expect(await loadRemembered('math')).toEqual({ username: 'bob', password: 'Passw0rd2' })
    expect(await loadRemembered('*')).toEqual({ username: 'root', password: 'Passw0rd3' })
    // 清除某域不影响其它域
    await clearRemembered('math')
    expect(await loadRemembered('math')).toBeNull()
    expect(await loadRemembered('tutor')).toEqual({ username: 'alice', password: 'Passw0rd1' })
  })

  it('兼容迁移：旧单域 key 有数据时迁移到当前域并清理旧 key', async () => {
    localStorage.setItem('agent.remembered_credentials', btoa(JSON.stringify({ username: 'old', password: 'OldPass1' })))
    expect(await loadRemembered('tutor')).toEqual({ username: 'old', password: 'OldPass1' })
    // 迁移后：当前域有数据、旧 key 已清理
    expect(localStorage.getItem('agent.remembered_credentials.tutor')).not.toBeNull()
    expect(localStorage.getItem('agent.remembered_credentials')).toBeNull()
  })
})
