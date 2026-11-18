// 단위(src 아래 spec)와 e2e(test 아래 e2e-spec)를 프로젝트로 나눈다.
const common = {
  moduleFileExtensions: ['js', 'json', 'ts'],
  transform: { '^.+\\.ts$': 'ts-jest' },
  testEnvironment: 'node',
};

module.exports = {
  projects: [
    { ...common, displayName: 'unit', rootDir: 'src', testRegex: '.*\\.spec\\.ts$' },
    { ...common, displayName: 'e2e', rootDir: 'test', testRegex: '.*\\.e2e-spec\\.ts$' },
  ],
};
