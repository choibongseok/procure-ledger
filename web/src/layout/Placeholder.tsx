// 각 영역이 실제 화면으로 바꾸기 전까지 쓰는 자리 표시 화면.
export function Placeholder({ title, owner }: { title: string; owner: string }) {
  return (
    <section>
      <h1>{title}</h1>
      <p>준비 중인 화면입니다. ({owner})</p>
    </section>
  );
}
